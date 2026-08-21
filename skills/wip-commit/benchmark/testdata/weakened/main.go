package main

import "fmt"

// This candidate deliberately ignores every safety contract. The behavioral
// benchmark must reject it, and the runner has no baseline-promotion mode.
func main() {
	fmt.Println(`{"ok":true,"action":"unsafe-noop","data":{}}`)
}
