/* ipp-usb - HTTP reverse proxy, backed by IPP-over-USB connection to device
 *
 * Copyright (C) 2020 and up by Alexander Pevzner (pzz@apevzner.com)
 * See LICENSE for license terms and conditions
 *
 * IPP service registration
 */

package main

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/alexpevzner/goipp"
)

// IppService performs IPP Get-Printer-Attributes query using provided
// http.Client and decodes received information into the form suitable
// for DNS-SD registration
func IppService(c *http.Client) (dnssd_name string, info DnsSdInfo, err error) {
	uri := "http://localhost/ipp/print"

	// Query printer attributes
	msg := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetPrinterAttributes, 1)
	msg.Operation.Add(goipp.MakeAttribute("attributes-charset",
		goipp.TagCharset, goipp.String("utf-8")))
	msg.Operation.Add(goipp.MakeAttribute("attributes-natural-language",
		goipp.TagLanguage, goipp.String("en-US")))
	msg.Operation.Add(goipp.MakeAttribute("printer-uri",
		goipp.TagURI, goipp.String(uri)))
	msg.Operation.Add(goipp.MakeAttribute("requested-attributes",
		goipp.TagKeyword, goipp.String("all")))

	req, _ := msg.EncodeBytes()
	resp, err := c.Post(uri, goipp.ContentType, bytes.NewBuffer(req))
	if err != nil {
		return
	}

	// Decode IPP response message
	respData, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return
	}

	err = msg.DecodeBytes(respData)
	if err != nil {
		log_debug("! IPP: %s", err)
		log_dump(respData)
		err = nil // FIXME - ignore error for now
		return
	}

	// Decode service info
	attrs := newIppDecoder(msg)
	dnssd_name, info = attrs.Decode()

	return
}

// ippAttrs represents a collection of IPP printer attributes,
// enrolled into a map for convenient access
type ippAttrs map[string]goipp.Values

// Create new ippAttrs
func newIppDecoder(msg *goipp.Message) ippAttrs {
	attrs := make(ippAttrs)

	// Note, we move from the end of list to the beginning, so
	// in a case of duplicated attributes, first occurrence wins
	for i := len(msg.Printer) - 1; i >= 0; i-- {
		attr := msg.Printer[i]
		attrs[attr.Name] = attr.Values
	}

	return attrs
}

// Decode printer attributes and build TXT record for IPP service
//
// This is where information comes from:
//
//   DNS-SD name: "printer-dns-sd-name" with fallback to
//                "printer-info" and "printer-make-and-model"
//
//   TXT fields:
//     air:              hardcoded as "none"
//     mopria-certified: "mopria-certified"
//     rp:               hardcoded as "ipp/print"
//     kind:             "printer-kind"
//     PaperMax:         based on decoding "media-size-supported"
//     URF:              "urf-supported" with fallback to
//                       URF extracted from "printer-device-id"
//     UUID:             "printer-uuid"
//     Color:            "color-supported"
//     Duplex:           search "sides-supported" for strings with
//                       prefix "one" or "two"
//     note:             "printer-location"
//     qtotal:           hardcoded as "1"
//     usb_MDL:          MDL, extracted from "printer-device-id"
//     usb_MFG:          MFG, extracted from "printer-device-id"
//     usb_CMD:          CMD, extracted from "printer-device-id"
//     ty:               "printer-make-and-model"
//     priority:         hardcoded as "50"
//     product:          "printer-make-and-model", in round brackets
//     pdl:              "document-format-supported"
//     txtvers:          hardcoded as "1"
//
func (attrs ippAttrs) Decode() (dnssd_name string, info DnsSdInfo) {
	info = DnsSdInfo{Type: "_ipp._tcp"}

	// Obtain dnssd_name
	dnssd_name = attrs.strSingle("printer-dns-sd-name",
		"printer-info", "printer-make-and-model")

	// Obtain and parse IEEE 1284 device ID
	devid := make(map[string]string)
	for _, id := range strings.Split(attrs.strSingle("printer-device-id"), ";") {
		keyval := strings.SplitN(id, ":", 2)
		if len(keyval) == 2 {
			devid[keyval[0]] = keyval[1]
		}
	}

	info.Txt.Add("air", "none")
	info.Txt.AddNotEmpty("mopria-certified", attrs.strSingle("mopria-certified"))
	info.Txt.Add("rp", "ipp/print")
	info.Txt.Add("priority", "50")
	info.Txt.AddNotEmpty("kind", attrs.strJoined("printer-kind"))
	info.Txt.AddNotEmpty("PaperMax", attrs.getPaperMax())
	if !info.Txt.AddNotEmpty("URF", attrs.strJoined("urf-supported")) {
		info.Txt.AddNotEmpty("URF", devid["URF"])
	}
	info.Txt.AddNotEmpty("UUID", strings.TrimPrefix(attrs.strSingle("printer-uuid"), "urn:uuid:"))
	info.Txt.AddNotEmpty("Color", attrs.getBool("color-supported"))
	info.Txt.AddNotEmpty("Duplex", attrs.getDuplex())
	info.Txt.Add("note", attrs.strSingle("printer-location"))
	info.Txt.Add("qtotal", "1")
	info.Txt.AddNotEmpty("usb_MDL", devid["MDL"])
	info.Txt.AddNotEmpty("usb_MFG", devid["MFG"])
	info.Txt.AddNotEmpty("usb_CMD", devid["CMD"])
	info.Txt.AddNotEmpty("ty", attrs.strSingle("printer-make-and-model"))
	info.Txt.AddNotEmpty("product", attrs.strBrackets("printer-make-and-model"))
	info.Txt.AddNotEmpty("pdl", attrs.strJoined("document-format-supported"))
	info.Txt.Add("txtvers", "1")

	log_debug("> %q: %s TXT record", dnssd_name, info.Type)
	for _, txt := range info.Txt {
		log_debug("  %s=%s", txt.Key, txt.Value)
	}

	return
}

// getDuplex returns "T" if printer supports two-sided
// printing, "F" if not and "" if it cant' tell
func (attrs ippAttrs) getDuplex() string {
	vals := attrs.getAttr(goipp.TypeString, "sides-supported")
	one, two := false, false
	for _, v := range vals {
		s := string(v.(goipp.String))
		switch {
		case strings.HasPrefix(s, "one"):
			one = true
		case strings.HasPrefix(s, "two"):
			two = true
		}
	}

	if two {
		return "T"
	}

	if one {
		return "F"
	}

	return ""
}

// getPaperMax returns max paper size, supported by printer
//
// According to Bonjour Printing Specification, Version 1.2.1,
// it can take one of following values:
//   "<legal-A4"
//   "legal-A4"
//   "tabloid-A3"
//   "isoC-A2"
//   ">isoC-A2"
//
// If PaperMax cannot be guessed, it returns empty string
func (attrs ippAttrs) getPaperMax() string {
	// Roll over "media-size-supported", extract
	// max x-dimension and max y-dimension
	vals := attrs.getAttr(goipp.TypeCollection, "media-size-supported")
	if vals == nil {
		return ""
	}

	var x_dim_max, y_dim_max int

	for _, collection := range vals {
		var x_dim_attr, y_dim_attr goipp.Attribute
		attrs := collection.(goipp.Collection)
		for i := len(attrs) - 1; i >= 0; i-- {
			switch attrs[i].Name {
			case "x-dimension":
				x_dim_attr = attrs[i]
			case "y-dimension":
				y_dim_attr = attrs[i]
			}
		}

		if len(x_dim_attr.Values) > 0 {
			switch dim := x_dim_attr.Values[0].V.(type) {
			case goipp.Integer:
				if int(dim) > x_dim_max {
					x_dim_max = int(dim)
				}
			case goipp.Range:
				if int(dim.Upper) > x_dim_max {
					x_dim_max = int(dim.Upper)
				}
			}
		}

		if len(y_dim_attr.Values) > 0 {
			switch dim := y_dim_attr.Values[0].V.(type) {
			case goipp.Integer:
				if int(dim) > y_dim_max {
					y_dim_max = int(dim)
				}
			case goipp.Range:
				if int(dim.Upper) > y_dim_max {
					y_dim_max = int(dim.Upper)
				}
			}
		}
	}

	if x_dim_max == 0 || y_dim_max == 0 {
		return ""
	}

	// Now classify by printer size
	//                  US name      US inches   US mm       ISO mm
	//   "legal-A4"     A, Legal     8.5 x 11    216 x 354   A4: 210 x 297
	//   "tabloid-A3"   B, Tabloid   11 x 17     279 x 432   A3: 297 x 420
	//   "isoC-A2"      C            17 × 22     432 × 559   A2: 420 x 594
	//
	// Please note, Apple in the "Bonjour Printing Specification"
	// incorrectly states paper sizes as 9x14, 13x19 and 18x24 inches

	const (
		legal_a4_x   = 21600
		legal_a4_y   = 35400
		tabloid_a3_x = 27900
		tabloid_a3_y = 43200
		isoC_a2_x    = 43200
		isoC_a2_y    = 55900
	)

	switch {
	case x_dim_max > isoC_a2_x && y_dim_max > isoC_a2_y:
		return ">isoC-A2"

	case x_dim_max >= isoC_a2_x && y_dim_max >= isoC_a2_y:
		return "isoC-A2"

	case x_dim_max >= tabloid_a3_x && y_dim_max >= tabloid_a3_y:
		return "tabloid-A3"

	case x_dim_max >= legal_a4_x && y_dim_max >= legal_a4_y:
		return "legal-A4"

	default:
		return "<legal-A4"
	}
}

// Get a single-string attribute
func (attrs ippAttrs) strSingle(names ...string) string {
	strs := attrs.getStrings(names...)
	if strs == nil {
		return ""
	}

	return strs[0]
}

// Get a multi-string attribute, represented as a comma-separated list
func (attrs ippAttrs) strJoined(names ...string) string {
	strs := attrs.getStrings(names...)
	return strings.Join(strs, ",")
}

// Get a single string, and put it into brackets
func (attrs ippAttrs) strBrackets(names ...string) string {
	s := attrs.strSingle(names...)
	if s != "" {
		s = "(" + s + ")"
	}
	return s
}

// Get attribute's []string value by attribute name
// Multiple names may be specified, for fallback purposes
func (attrs ippAttrs) getStrings(names ...string) []string {
	vals := attrs.getAttr(goipp.TypeString, names...)
	strs := make([]string, len(vals))
	for i := range vals {
		strs[i] = string(vals[i].(goipp.String))
	}

	return strs
}

// Get boolean attribute. Returns "F" or "T" if attribute is found,
// empty string otherwise.
// Multiple names may be specified, for fallback purposes
func (attrs ippAttrs) getBool(names ...string) string {
	vals := attrs.getAttr(goipp.TypeBoolean, names...)
	if vals == nil {
		return ""
	}
	if vals[0].(goipp.Boolean) {
		return "T"
	}
	return "F"
}

// Get attribute's value by attribute name
// Multiple names may be specified, for fallback purposes
// Value type is checked and enforced
func (attrs ippAttrs) getAttr(t goipp.Type, names ...string) []goipp.Value {

	for _, name := range names {
		v, ok := attrs[name]
		if ok && v[0].V.Type() == t {
			var vals []goipp.Value
			for i := range v {
				vals = append(vals, v[i].V)
			}
			return vals
		}
	}

	return nil
}
