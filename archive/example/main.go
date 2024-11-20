// Copyright (c) seasonjs. All rights reserved.
// Licensed under the MIT License. See License.txt in the project root for license information.

package main

import (
	"fmt"
	"log"

	"github.com/cozy-creator/hf-hub/hub"
)

func main() {
	client := hub.DefaultClient()
	repo := hub.NewRepo("black-forest-labs/FLUX.1-schnell")
	params := &hub.DownloadParams{Repo: repo, FileName: "config.json",}

	path, err := client.Download(params)
	if err != nil {
		log.Println(err)
	}

	fmt.Println(path)

}
