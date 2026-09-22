package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/static/* web/templates/*
var embeddedWeb embed.FS

func browserAssetsHandler() http.Handler {
	static, err := fs.Sub(embeddedWeb, "web/static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/assets/", http.FileServer(http.FS(static)))
}

//go:embed web/templates/login.html
var loginPage string

//go:embed web/templates/login_error.html
var loginPageWithError string

const browserCSS = `<link rel="stylesheet" href="/assets/browser.css">`
const browserBoardScript = `<script src="/assets/board.js" defer></script>`
const browserDetailScript = `<script src="/assets/detail.js" defer></script>`
