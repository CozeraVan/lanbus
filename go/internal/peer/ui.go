package peer

import _ "embed"

// approvalHTML is the loopback-only human approval inbox served at /peer/ui.
// The secret arrives via the URL fragment (#secret=...) which never leaves
// the browser; `lanbus peer-ui` opens the page with it pre-filled.
//
//go:embed approval.html
var approvalHTML string