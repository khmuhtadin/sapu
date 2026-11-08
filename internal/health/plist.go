package health

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// parsePlist reads an XML property list, which is what `ioreg -a` and
// `diskutil info -plist` print. It returns the top-level value: a
// map[string]any, a []any, a string, an int64, a float64 or a bool. Dates
// and data come back as their text.
func parsePlist(data []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	inPlist := false
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("not a property list: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !inPlist {
			inPlist = start.Name.Local == "plist"
			continue
		}
		return plistValue(d, start)
	}
}

func plistValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return m, nil
			case xml.StartElement:
				if t.Name.Local == "key" {
					if key, err = plistText(d); err != nil {
						return nil, err
					}
					continue
				}
				v, err := plistValue(d, t)
				if err != nil {
					return nil, err
				}
				m[key] = v
			}
		}
	case "array":
		list := []any{}
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return list, nil
			case xml.StartElement:
				v, err := plistValue(d, t)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
		}
	case "true", "false":
		return start.Name.Local == "true", d.Skip()
	case "integer":
		text, err := plistText(d)
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	case "real":
		text, err := plistText(d)
		if err != nil {
			return nil, err
		}
		return strconv.ParseFloat(strings.TrimSpace(text), 64)
	case "string", "date", "data":
		return plistText(d)
	}
	return nil, errors.New("unknown property list element " + start.Name.Local)
}

// plistText reads the text up to the end of the current element.
func plistText(d *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

// intOf returns v as an int, or 0 when it is not a number.
func intOf(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
