package main

import (
	"log"
	"os"

	"github.com/trustacks/catalog/pkg/catalog"
	"github.com/trustacks/catalog/server"
)

var (
	mode      = os.Getenv("CATALOG_MODE")
	component = os.Getenv("HOOK_COMPONENT")
	hookKind  = os.Getenv("HOOK_KIND")
)

func main() {
	switch mode {
	case "hook":
		if err := catalog.Call(component, hookKind); err != nil {
			log.Fatal(err)
		}
	default:
		server.StartCatalogServer()
	}
}