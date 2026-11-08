// Package ruledata embeds the built-in rule files into the binary. To get the
// rules in use (these, or a newer signed bundle from `sapu rules install`),
// call rulesupdate.Active instead of reading FS directly.
// Use fs.Sub(ruledata.FS, "cleaners"), "protect" or "purge" for the built-in files.
package ruledata

import "embed"

//go:embed cleaners protect purge
var FS embed.FS
