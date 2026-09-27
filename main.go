//go:build !server

package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"os"

	"mintswitch/internal/enhance"
	"mintswitch/internal/service"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
// See https://pkg.go.dev/embed for more information.

//go:embed all:frontend/dist
var assets embed.FS

// main is the desktop application entry point. When invoked as
// `MintSwitch enhance-prompt --tool <id>` (by the /enhance slash command
// MintSwitch installs into the managed tools) it runs the headless
// enhance-prompt client instead of opening a window and exits.
func main() {
	if len(os.Args) > 1 && os.Args[1] == enhance.CLIName {
		os.Exit(runEnhanceCLI(os.Args[2:]))
	}

	// Build the backend service that exposes tool management to the frontend.
	// A failure here means the user's home/data directories could not be resolved,
	// so there is nothing the app can usefully do; log and exit.
	svc, err := service.New()
	if err != nil {
		log.Fatal(err)
	}

	// Create a new Wails application by providing the necessary options.
	// Variables 'Name' and 'Description' are for application metadata.
	// 'Assets' configures the asset server with the 'FS' variable pointing to the frontend files.
	// 'Bind' is a list of Go struct instances. The frontend has access to the methods of these instances.
	// 'Mac' options tailor the application when running an macOS.
	app := application.New(application.Options{
		Name:        "MintSwitch",
		Description: "AI Tool API Config Switcher",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// Create a new window with the necessary options.
	// 'Title' is the title of the window.
	// 'Mac' options tailor the window when running on macOS.
	// 'BackgroundColour' is the background colour of the window.
	// 'URL' is the URL that will be loaded into the webview.
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "MintSwitch",
		// Window sized to the golden ratio (1000 / 618 ≈ 1.618).
		Width:  1000,
		Height: 618,
		// Lock the minimum size to the launch size so the window can't be
		// dragged smaller than the comfortable 2-column layout.
		MinWidth:  1000,
		MinHeight: 618,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(6, 7, 15),
		URL:              "/",
	})

	// Run the application. This blocks until the application has been exited.
	err = app.Run()

	// If an error occurred while running the application, log it and exit.
	if err != nil {
		log.Fatal(err)
	}
}

// runEnhanceCLI parses `--tool <id>`, reads the rough prompt from stdin and
// prints the enhanced prompt on stdout. Errors go to stderr prefixed with
// "enhance-prompt:" so the slash-command templates can recognise them; the
// exit code is 2 for usage errors and 1 for everything else.
func runEnhanceCLI(args []string) int {
	fs := flag.NewFlagSet(enhance.CLIName, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tool := fs.String("tool", "", "tool ID whose effective provider is used (claude-code, codex, opencode, pi)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	store, err := service.SettingsStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", enhance.CLIName, err)
		return 1
	}
	if err := enhance.RunCLI(context.Background(), store, *tool, os.Stdin, os.Stdout, nil); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", enhance.CLIName, err)
		return 1
	}
	return 0
}
