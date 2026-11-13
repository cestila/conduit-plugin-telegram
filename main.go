package main

import "github.com/cestila/ConduitOmniChat/sdk/pluginsdk"

func main() {
	pluginsdk.Serve(NewTelegramPlugin())
}
