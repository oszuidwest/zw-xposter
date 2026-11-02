module github.com/oszuidwest/zw-xposter

go 1.27.3

// npm packages under node_modules may contain Go sources.
ignore ./poster/node_modules

require github.com/oszuidwest/zw-graphmail v0.0.2

require golang.org/x/oauth2 v0.37.0 // indirect
