package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func main() {
	// Create a new application
	app := tview.NewApplication()

	// Create a scrollable TextView
	textView := tview.NewTextView().
		SetDynamicColors(true). // Allow color formatting
		SetScrollable(true).    // Enable scrolling
		SetChangedFunc(func() { // Ensure the view updates when the content changes
			app.Draw()
		})

	// Add a lot of content to the TextView
	longText := strings.Repeat("This is a line in the scrollable view.\n", 100)
	fmt.Fprint(textView, longText)

	// Add instructions at the top for navigation
	instructions := "[yellow]Use arrow keys to scroll, or press 'q' to quit.[-]\n\n"
	fmt.Fprint(textView, instructions)

	// Create a layout with just the scrollable TextView
	layout := tview.NewFlex().
		AddItem(textView, 0, 1, true)

	// Capture key events for scrolling and quitting
	textView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyESC, tcell.KeyCtrlC:
			app.Stop() // Quit the application on 'Esc' or Ctrl+C
		case tcell.KeyRune:
			if event.Rune() == 'q' {
				app.Stop() // Quit the application on 'q'
			}
		}
		return event
	})

	// Run the application
	if err := app.SetRoot(layout, true).Run(); err != nil {
		log.Fatalf("Error launching application: %v", err)
	}
}
