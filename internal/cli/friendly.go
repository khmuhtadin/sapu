package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/khmuhtadin/sapu/internal/fsop"
)

const lockedText = "Another sapu is working on this Mac right now. Try again when it finishes."

// friendlyErr turns the errors people can run into into plain sentences. It
// returns err's own text and an empty code for every other error.
func friendlyErr(err error) (text, code string) {
	switch {
	case errors.Is(err, fsop.ErrLocked):
		return lockedText, "locked"
	case errors.Is(err, fsop.ErrUnavailable):
		return unavailableText(err), "unavailable"
	}
	return err.Error(), ""
}

// unavailableText names the volume that is not connected. fsop words the
// error "<ErrUnavailable>: <mount> holds items of run <id>; connect it and try again".
func unavailableText(err error) string {
	volume := "A volume"
	_, rest, _ := strings.Cut(err.Error(), fsop.ErrUnavailable.Error()+": ")
	if mount, _, ok := strings.Cut(rest, " holds items of run "); ok {
		volume = "The volume " + mount
	}
	return volume + " that holds quarantined items is not connected. " +
		"Connect it and try again; its items can still be restored with sapu undo."
}

// failureText explains a failed item. Something replaced a stranded item
// while it moved, so it sits in quarantine until undo puts it back.
func failureText(runID, failure string) string {
	if strings.HasPrefix(failure, fsop.ErrStranded.Error()) {
		return fmt.Sprintf("something replaced this item while it moved. It is in quarantine; `sapu undo %s` puts it back", runID)
	}
	return failure
}
