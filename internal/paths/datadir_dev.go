//go:build !production

package paths

// dataDirName is the directory name (under os.UserConfigDir()) holding
// MintSwitch's own data in development builds (`wails3 dev`, plain `go build`,
// `go test` — anything without -tags production). It is deliberately distinct
// from the production "mintswitch" dir so dev data never leaks into (or
// clobbers) a real user's profiles and keys.
const dataDirName = "mintswitch-dev"
