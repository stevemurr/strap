// Strap-sidebar is an offline playground for terminal sidebar alternatives.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stevemurr/strap/internal/tui"
)

func main() {
	snapshot := flag.Bool("snapshot", false, "print a static terminal frame")
	export := flag.Bool("export", false, "export cell-accurate browser preview data as JSON")
	kind := flag.Int("concept", 1, "concept: 1 stacks, 2 signal rail, 3 branch map")
	focus := flag.Int("focus", -1, "agent to peek at in a snapshot (0–7)")
	flag.Parse()
	var err error
	if *export {
		lipgloss.SetColorProfile(termenv.TrueColor)
		err = json.NewEncoder(os.Stdout).Encode(tui.SidebarLabPreview())
	} else if *snapshot {
		fmt.Println(tui.SidebarLabSnapshot(*kind-1, *focus))
	} else {
		err = tui.RunSidebarLab()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
