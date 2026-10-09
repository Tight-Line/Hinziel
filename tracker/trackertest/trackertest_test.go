package trackertest

import (
	"testing"

	"github.com/Tight-Line/heinzel/tracker"
)

func TestFakeConforms(t *testing.T) {
	Run(t, NewFake(tracker.Ticket{Key: "T-1", Title: "Add export"}), "T-1")
}
