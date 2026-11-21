// Package buildychat holds the files the desktop host embeds from its pinned dependency, buildytech/chat
// (git submodule third_party/chat): the built web app and the runtime HTTP contract. Nothing here is edited by hand:
// frontend/dist is produced by scripts/build-frontend.sh from the submodule, the contract lives in the submodule.
package buildychat

import (
	"embed"
	"io/fs"
)

//go:embed all:frontend/dist
var frontend embed.FS

//go:embed third_party/chat/contract/runtime-http
var contract embed.FS

// Frontend is the built apps/chat-web site (empty until scripts/build-frontend.sh ran).
func Frontend() fs.FS {
	sub, err := fs.Sub(frontend, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Contract is contract/runtime-http of the pinned chat commit: cases/, constants.json, pictures/.
func Contract() fs.FS {
	sub, err := fs.Sub(contract, "third_party/chat/contract/runtime-http")
	if err != nil {
		panic(err)
	}
	return sub
}
