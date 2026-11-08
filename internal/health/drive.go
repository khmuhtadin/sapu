package health

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

var wholeDiskRe = regexp.MustCompile(`^disk\d+`)

func checkDrive(ctx context.Context) Row {
	row := Row{ID: "drive", Title: "Startup drive"}
	out, err := RunCommand(ctx, "diskutil", "info", "-plist", "/")
	if err != nil {
		return unknownRow(row.ID, row.Title, err)
	}
	v, err := parsePlist(out)
	if err != nil {
		return unknownRow(row.ID, row.Title, err)
	}
	info, ok := v.(map[string]any)
	if !ok {
		return unknownRow(row.ID, row.Title, errors.New("diskutil printed an unexpected answer"))
	}
	solid, _ := info["SolidState"].(bool)
	smart, _ := info["SMARTStatus"].(string)
	model, trim := "", ""

	// The NVMe list has the model and TRIM. Find the drive that holds "/".
	if disk := physicalDisk(info); disk != "" {
		if out, err := RunCommand(ctx, "system_profiler", "SPNVMeDataType", "-json"); err == nil {
			model, trim = nvmeDrive(out, disk, &smart)
		}
	}

	var parts []string
	switch {
	case model != "":
		parts = append(parts, model)
	case solid:
		parts = append(parts, "SSD")
	default:
		parts = append(parts, "hard disk")
	}
	if smart != "" {
		parts = append(parts, "SMART "+smart)
	}
	if trim != "" {
		parts = append(parts, "TRIM "+strings.ToLower(trim))
	}
	row.Value = strings.Join(parts, ", ")

	switch smart {
	case "Verified":
		row.Status = Good
		row.Meaning = "The drive reports a healthy SMART status. SMART is a basic check that cannot predict every failure, so keep a backup."
	case "Failing":
		row.Status = Attention
		row.Meaning = "The drive reports that it is failing. Back it up now and contact Apple Support."
	default:
		row.Status = Unknown
		row.Meaning = "macOS does not report a SMART status for this drive, so Sapu cannot tell how healthy it is."
	}
	if solid && strings.EqualFold(trim, "No") {
		row.Status = worse(row.Status, Watch)
		row.Meaning += " TRIM is off, which can slow writes as the drive fills."
	}
	return row
}

// physicalDisk returns the whole disk that holds "/", like "disk0".
func physicalDisk(info map[string]any) string {
	slice := ""
	if stores, _ := info["APFSPhysicalStores"].([]any); len(stores) > 0 {
		store, _ := stores[0].(map[string]any)
		slice, _ = store["APFSPhysicalStore"].(string)
	}
	if slice == "" {
		slice, _ = info["ParentWholeDisk"].(string)
	}
	return wholeDiskRe.FindString(slice)
}

// nvmeDrive finds the drive named disk in `system_profiler SPNVMeDataType
// -json` and returns its model and TRIM support. A SMART status it reports
// replaces *smart. Both results are "" when the drive is not in the list.
func nvmeDrive(out []byte, disk string, smart *string) (model, trim string) {
	type drive struct {
		BSDName string `json:"bsd_name"`
		Model   string `json:"device_model"`
		Smart   string `json:"smart_status"`
		Trim    string `json:"spnvme_trim_support"`
	}
	var doc struct {
		Controllers []struct {
			Items []drive `json:"_items"`
		} `json:"SPNVMeDataType"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return "", ""
	}
	for _, c := range doc.Controllers {
		for _, d := range c.Items {
			if d.BSDName == disk {
				if d.Smart != "" {
					*smart = d.Smart
				}
				return d.Model, d.Trim
			}
		}
	}
	return "", ""
}
