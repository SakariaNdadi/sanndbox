package main

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
)

//go:embed templates/index.html
var templateFS embed.FS

// @title Communications Sanndbox Portal
// @version 1.0.0
// @description Unified Landing Portal for Email (:9081), SMS (:9082), and IM (:9083) Sanndbox Servers.
// @BasePath /

func main() {
	parsedTemplates := template.Must(template.ParseFS(templateFS, "templates/index.html"))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = parsedTemplates.ExecuteTemplate(responseWriter, "index.html", nil)
	})

	fmt.Println("[portal-server] Sanndbox Hub listening on http://localhost:9080")
	_ = http.ListenAndServe(":9080", mux)
}
