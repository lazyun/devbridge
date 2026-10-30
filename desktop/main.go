package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Dev Bridge Desktop",
		Width:     1080,
		Height:    720,
		MinWidth:  860,
		MinHeight: 580,
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: app.consoleEmbedMiddleware,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
