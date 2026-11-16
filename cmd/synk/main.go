package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/bristlecone-zSuite/zSynk/internal/api"
	"github.com/bristlecone-zSuite/zSynk/internal/apps"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
	"github.com/bristlecone-zSuite/zSynk/internal/logfile"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "serve":
		err = cmdServe(args)
	case "status":
		err = cmdStatus(args)
	case "apps":
		err = cmdApps(args)
	case "enable":
		err = cmdEnable(args)
	case "pair-offer":
		err = cmdPairOffer(args)
	case "pair":
		err = cmdPair(args)
	case "sync":
		err = cmdSync(args)
	case "doctor":
		err = cmdDoctor(args)
	case "log":
		err = cmdLog(args)
	case "confirm-deletes":
		err = cmdHeldDeletes("confirm-deletes", args)
	case "restore-deletes":
		err = cmdHeldDeletes("restore-deletes", args)
	case "pause":
		err = cmdPause(args)
	case "resume":
		err = cmdResume(args)
	case "remove":
		err = cmdRemove(args)
	case "unpair":
		err = cmdUnpair(args)
	case "versions":
		err = cmdVersions(args)
	case "restore":
		err = cmdRestore(args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %s\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "synk: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Synk — keep writing, not wiring.

Usage:
  synk init [--name NAME]
  synk serve [-v]
  synk status
  synk apps
  synk enable --preset ID [--path DIR] [--id ID] [--label LABEL] [--share-with DEVICE,...|all]
  synk pair-offer
  synk pair --uri synk://pair?...
  synk pair --addr HOST:PORT --code CODE --token TOKEN [--fp FINGERPRINT]
  synk sync --addr HOST:PORT --folder ID
  synk doctor
  synk log [-n LINES]
  synk confirm-deletes --library ID
  synk restore-deletes --library ID
  synk pause --library ID
  synk resume --library ID
  synk remove --library ID
  synk unpair --device ID
  synk versions --library ID [--path PATH]
  synk restore --library ID --path PATH [--version VERSION]

Before a sync replaces or deletes a file, Synk keeps a copy: synk versions
lists them, and synk restore puts one back. synk pause stops a library
syncing until synk resume; synk remove stops syncing it and keeps its
files, its versions, and its index; synk unpair forgets a paired device.

While synk serve is running, the other commands go through its local API.
What Synk changes and what goes wrong is logged to synk.log in the data
directory, which synk log shows; synk serve -v logs every sync as well.

Data directory: $SYNK_DIR or platform default.
`)
}

func dataDir() string {
	if d := os.Getenv("SYNK_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".synk"
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Synk")
	}
	return filepath.Join(home, ".local", "share", "synk")
}

func openNode(name string) (*engine.Node, error) {
	if name == "" {
		name, _ = os.Hostname()
	}
	return engine.Open(dataDir(), name)
}

// target is how a command reaches Synk: the node itself, or, while
// `synk serve` has the data folder open, that daemon's API.
type target struct {
	node   *engine.Node
	daemon *api.Client
	logs   *logfile.File
}

func connect(name string) (*target, error) {
	n, err := openNode(name)
	if err == nil {
		// With the data folder open here, no daemon is writing the log.
		logs, err := openLog(n, slog.LevelInfo, nil)
		if err != nil {
			_ = n.Close()
			return nil, err
		}
		return &target{node: n, logs: logs}, nil
	}
	if !errors.Is(err, engine.ErrInUse) {
		return nil, err
	}
	cfg, cerr := config.Load(dataDir())
	if cerr != nil {
		return nil, cerr
	}
	c, cerr := api.Connect(dataDir(), cfg.API)
	if cerr != nil {
		return nil, fmt.Errorf("%w, and its API can't be used: %v", err, cerr)
	}
	return &target{daemon: c}, nil
}

func (t *target) close() {
	if t.node != nil {
		_ = t.node.Close()
	}
	if t.logs != nil {
		_ = t.logs.Close()
	}
}

// status returns the device's status. Without a daemon, which keeps its
// libraries scanned, scan says whether to scan them first.
func (t *target) status(scan bool) (engine.Status, error) {
	if t.daemon != nil {
		return t.daemon.Status()
	}
	if scan {
		for _, l := range t.node.Libraries() {
			// A failed scan shows up in the library's status.
			_, _ = t.node.ScanLibrary(l.ID)
		}
	}
	return t.node.Status(), nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "device name")
	_ = fs.Parse(args)
	t, err := connect(*name)
	if err != nil {
		return err
	}
	defer t.close()
	st, err := t.status(false)
	if err != nil {
		return err
	}
	fmt.Printf("Device %s (%s)\nData %s\n", st.Name, st.DeviceID, dataDir())
	return nil
}

// logLimit bounds synk.log; the log before it is kept as synk.log.1.
const logLimit = 4 << 20

func logPath(dataDir string) string { return filepath.Join(dataDir, "synk.log") }

// openLog sends n's log to synk.log in its data folder, and to also if it
// isn't nil.
func openLog(n *engine.Node, level slog.Level, also io.Writer) (*logfile.File, error) {
	logs, err := logfile.Open(logPath(n.DataDir), logLimit)
	if err != nil {
		return nil, err
	}
	var w io.Writer = logs
	if also != nil {
		w = io.MultiWriter(also, logs)
	}
	n.Log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
	return logs, nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	verbose := fs.Bool("v", false, "log every sync, not only changes and problems")
	_ = fs.Parse(args)
	n, err := openNode("")
	if err != nil {
		return err
	}
	defer n.Close()
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logs, err := openLog(n, level, os.Stderr)
	if err != nil {
		return err
	}
	defer logs.Close()
	srv, err := api.New(n)
	if err != nil {
		return err
	}
	st := n.Status()
	// Other commands reach a running daemon only through its API.
	if err := srv.Listen(st.API); err != nil {
		return fmt.Errorf("local API: %w", err)
	}
	ln, err := n.ServePeers(st.Listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Printf("Synk %s listening for peers on %s\n", n.Device.DeviceID, ln.Addr())
	fmt.Printf("Local API on http://%s (token in %s)\n", srv.Addr(), api.TokenPath(n.DataDir))
	n.Log.Info("started", "device", n.Device.Name, "peers", ln.Addr().String(), "api", srv.Addr())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	looped := make(chan struct{})
	go func() {
		defer close(looped)
		n.WatchAndSync(ctx)
	}()
	go func() {
		if err := srv.Serve(); err != nil {
			fmt.Fprintf(os.Stderr, "api: %v\n", err)
		}
	}()
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	go func() {
		<-ch
		fmt.Fprintln(os.Stderr, "synk: stopping now")
		os.Exit(1)
	}()
	fmt.Println("Stopping; a second interrupt stops at once.")
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = srv.Shutdown(shutdown)
	// The data folder stays locked until the loop stops writing to it.
	<-looped
	return nil
}

func cmdStatus(args []string) error {
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	st, err := t.status(true)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(st)
}

func cmdApps(args []string) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"catalog": apps.Catalog(), "discovered": apps.Discover()})
}

func cmdEnable(args []string) error {
	fs := flag.NewFlagSet("enable", flag.ExitOnError)
	preset := fs.String("preset", "", "preset id")
	path := fs.String("path", "", "folder")
	id := fs.String("id", "", "library id")
	label := fs.String("label", "", "label")
	share := fs.String("share-with", "", "comma-separated device IDs, or all")
	_ = fs.Parse(args)
	if *preset == "" {
		return fmt.Errorf("enable: --preset is required")
	}
	p, ok := apps.ByID(*preset)
	if !ok {
		return fmt.Errorf("unknown preset %s", *preset)
	}
	shareWith, err := shareList(*share)
	if err != nil {
		return err
	}
	lib := config.Library{Preset: *preset, Label: *label, Path: *path, ID: *id, ShareWith: shareWith}
	if lib.ID == "" {
		lib.ID = *preset
	}
	if lib.Path == "" {
		if lib.Path, err = apps.DefaultPath(*preset); err != nil {
			return fmt.Errorf("enable: %w (use --path)", err)
		}
	}
	// A daemon would resolve a relative path against its own folder.
	if lib.Path, err = filepath.Abs(lib.Path); err != nil {
		return err
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	if t.daemon != nil {
		lib, err = t.daemon.EnableLibrary(lib)
	} else if err = t.node.EnableLibrary(lib); err == nil {
		lib, _ = t.node.Library(lib.ID)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Enabled %s at %s\n", lib.Label, lib.Path)
	if p.Warning != "" {
		fmt.Printf("Note: %s\n", p.Warning)
	}
	return nil
}

// shareList reads --share-with: device IDs, or all. Without it, a library
// that's already enabled keeps its list.
func shareList(s string) ([]string, error) {
	switch s {
	case "":
		return nil, nil
	case "all":
		return []string{}, nil
	}
	ids := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(ids) == 0 {
		return nil, fmt.Errorf("enable: --share-with takes device IDs, or all")
	}
	return ids, nil
}

func cmdPairOffer(args []string) error {
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	var offer api.Offer
	if t.daemon != nil {
		offer, err = t.daemon.StartPairing()
	} else {
		offer, err = api.StartPairing(t.node)
	}
	if err != nil {
		return err
	}
	fmt.Printf("On this device: %s (%s)\n", offer.Name, offer.DeviceID)
	fmt.Printf("Code        %s\nToken       %s\nFingerprint %s\nLink        %s\n", offer.Code, offer.Token, offer.Fingerprint, offer.QR)
	if t.daemon != nil {
		fmt.Println("The offer is good for one hour. On the other device, run:")
	} else {
		fmt.Println("The offer is saved for one hour. Start synk serve here, then on the other device run:")
	}
	addr := "HOST:7420"
	if len(offer.Addresses) > 0 {
		addr = offer.Addresses[0]
	}
	fmt.Printf("  synk pair --addr %s --code %s --token %s --fp %s\n", addr, offer.Code, offer.Token, offer.Fingerprint)
	return nil
}

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	uri := fs.String("uri", "", "synk://pair link from the other device")
	addr := fs.String("addr", "", "host:port")
	code := fs.String("code", "", "pairing code")
	token := fs.String("token", "", "pairing token")
	fp := fs.String("fp", "", "the other device's key fingerprint")
	_ = fs.Parse(args)
	var addrs []string
	if *uri != "" {
		l, err := pairing.ParseURI(*uri)
		if err != nil {
			return err
		}
		*code, *token, *fp, addrs = l.Code, l.Token, l.Fingerprint, l.Addrs
	}
	if *addr != "" {
		addrs = []string{*addr}
	}
	if len(addrs) == 0 || *code == "" || *token == "" {
		return fmt.Errorf("pair: give --uri, or --addr, --code, and --token")
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	for _, a := range addrs {
		if t.daemon != nil {
			err = t.daemon.JoinPeer(a, *code, *token, *fp)
		} else {
			err = t.node.JoinPeer(a, *code, *token, *fp)
		}
		if err == nil {
			fmt.Printf("Paired with the device at %s\n", a)
			return nil
		}
		if rejected(err) {
			return err
		}
	}
	return err
}

// rejected reports whether pairing failed because of the offer rather than
// because the address didn't answer, so trying another address won't help.
func rejected(err error) bool {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status != http.StatusBadGateway
	}
	return errors.Is(err, engine.ErrPairingRejected)
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	addr := fs.String("addr", "", "host:port")
	folder := fs.String("folder", "", "library id")
	_ = fs.Parse(args)
	if *addr == "" || *folder == "" {
		return fmt.Errorf("sync: --addr and --folder required")
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	if t.daemon != nil {
		return t.daemon.Sync(*addr, *folder)
	}
	return t.node.SyncWith(*addr, *folder)
}

func cmdDoctor(args []string) error {
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	st, err := t.status(true)
	if err != nil {
		return err
	}
	fmt.Printf("Device %s (%s)\n", st.Name, st.DeviceID)
	if t.daemon != nil {
		fmt.Println("synk serve is running.")
	} else {
		fmt.Println("synk serve isn't running, so nothing syncs.")
	}
	if len(st.Peers) == 0 {
		fmt.Println("No paired devices yet.")
	} else {
		names := make([]string, 0, len(st.Peers))
		for _, p := range st.Peers {
			names = append(names, p.Name)
		}
		fmt.Printf("Paired with %s.\n", strings.Join(names, ", "))
	}
	for _, l := range st.Libraries {
		fmt.Printf("Library %s (%s) %s — %d files\n", l.Label, l.ID, l.Path, l.Files)
		// Without the daemon, this process hasn't synced anything.
		if t.daemon != nil {
			for _, d := range l.Devices {
				fmt.Printf("  %s\n", syncState(d, time.Now()))
			}
		}
	}
	report := api.Diagnose(st)
	if report.OK {
		fmt.Println("No problems found.")
		return nil
	}
	fmt.Println("Problems:")
	for _, issue := range report.Issues {
		fmt.Printf("  %s\n", issue)
	}
	for _, l := range st.Libraries {
		if len(l.HeldDeletes) > 0 {
			fmt.Printf("Review %s's held deletes with synk status, then run synk confirm-deletes --library %s, or synk restore-deletes --library %s.\n", l.Label, l.ID, l.ID)
		}
	}
	return nil
}

// syncState says how a library has synced with a device, as of now.
func syncState(d engine.DeviceSync, now time.Time) string {
	last := "hasn't synced since synk serve started"
	if !d.LastSync.IsZero() {
		last = "synced " + now.Sub(d.LastSync).Round(time.Second).String() + " ago"
	}
	if d.Failures == 0 {
		return d.Name + ": " + last
	}
	next := "soon"
	if wait := d.RetryAt.Sub(now).Round(time.Second); wait > 0 {
		next = "in " + wait.String()
	}
	return fmt.Sprintf("%s: %s; failing (%d in a row), next try %s", d.Name, last, d.Failures, next)
}

func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 50, "how many lines to show")
	_ = fs.Parse(args)
	lines, err := logfile.Tail(logPath(dataDir()), *n)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		fmt.Printf("Nothing is logged yet. synk serve logs to %s.\n", logPath(dataDir()))
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

func cmdHeldDeletes(name string, args []string) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	id := fs.String("library", "", "library id")
	_ = fs.Parse(args)
	if *id == "" {
		return fmt.Errorf("%s: --library is required", name)
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	var paths []string
	switch {
	case name == "confirm-deletes" && t.daemon != nil:
		var st engine.Status
		if st, err = t.daemon.Status(); err == nil {
			paths, err = t.daemon.ConfirmDeletes(*id, heldIn(st, *id))
		}
	case name == "confirm-deletes":
		paths, err = t.node.ConfirmDeletes(*id, nil)
	case t.daemon != nil:
		var r api.Restored
		r, err = t.daemon.RestoreDeletes(*id)
		paths = r.Paths
		if r.Warning != "" {
			err = errors.New(r.Warning)
		}
	default:
		paths, err = t.node.RestoreDeletes(*id)
	}
	for _, p := range paths {
		fmt.Println(p)
	}
	if len(paths) > 0 && err != nil && name == "restore-deletes" {
		fmt.Fprintf(os.Stderr, "Some files couldn't be put back here; they come back from a paired device at the next sync: %v\n", err)
		return nil
	}
	if len(paths) == 0 && err == nil {
		fmt.Printf("No deletes are held for %s.\n", *id)
	}
	return err
}

func heldIn(st engine.Status, id string) []string {
	for _, l := range st.Libraries {
		if l.ID == id {
			return l.HeldDeletes
		}
	}
	return nil
}

// withLibrary runs f with the ID the --library flag names, through the
// daemon when one is running.
func withLibrary(name string, args []string, f func(t *target, id string) error) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	id := fs.String("library", "", "library id")
	_ = fs.Parse(args)
	if *id == "" {
		return fmt.Errorf("%s: --library is required", name)
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	return f(t, *id)
}

func cmdPause(args []string) error {
	return withLibrary("pause", args, func(t *target, id string) error {
		if t.daemon != nil {
			if _, err := t.daemon.PauseLibrary(id); err != nil {
				return err
			}
		} else if err := t.node.PauseLibrary(id); err != nil {
			return err
		}
		fmt.Printf("Paused %s. Nothing syncs until synk resume --library %s.\n", id, id)
		return nil
	})
}

func cmdResume(args []string) error {
	return withLibrary("resume", args, func(t *target, id string) error {
		if t.daemon != nil {
			if _, err := t.daemon.ResumeLibrary(id); err != nil {
				return err
			}
		} else if err := t.node.ResumeLibrary(id); err != nil {
			return err
		}
		fmt.Printf("Resumed %s. What changed while it was paused syncs now.\n", id)
		return nil
	})
}

// cmdRemove forgets a library. Synk keeps the files, the versions in the
// vault, and the index, and says so.
func cmdRemove(args []string) error {
	return withLibrary("remove", args, func(t *target, id string) error {
		if t.daemon != nil {
			if err := t.daemon.RemoveLibrary(id); err != nil {
				return err
			}
		} else if err := t.node.RemoveLibrary(id); err != nil {
			return err
		}
		fmt.Printf("Removed %s. Synk keeps its files, its versions, and its index;\n", id)
		fmt.Println("enabling the folder again picks up where it left off.")
		return nil
	})
}

// withDevice runs f with the ID the --device flag names.
func withDevice(name string, args []string, f func(t *target, id string) error) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	id := fs.String("device", "", "device id")
	_ = fs.Parse(args)
	if *id == "" {
		return fmt.Errorf("%s: --device is required", name)
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	return f(t, *id)
}

// cmdUnpair forgets a paired device. Synk keeps every file.
func cmdUnpair(args []string) error {
	return withDevice("unpair", args, func(t *target, id string) error {
		if t.daemon != nil {
			if err := t.daemon.Unpair(id); err != nil {
				return err
			}
		} else if err := t.node.Unpair(id); err != nil {
			return err
		}
		fmt.Printf("Unpaired %s. Nothing synced with it changes, and it can pair\n", id)
		fmt.Println("again with a new offer.")
		return nil
	})
}

func cmdVersions(args []string) error {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	id := fs.String("library", "", "library id")
	path := fs.String("path", "", "a file, relative to the library or absolute; without it, every file")
	_ = fs.Parse(args)
	if *id == "" {
		return fmt.Errorf("versions: --library is required")
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	rel, err := t.inLibrary(*id, *path)
	if err != nil {
		return err
	}
	var vs []vault.Version
	if t.daemon != nil {
		vs, err = t.daemon.Versions(*id, rel)
	} else {
		vs, err = t.node.Versions(*id, rel)
	}
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		fmt.Println("No versions are kept.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEPT\tBEFORE IT WAS\tSIZE\tPATH\tVERSION")
	for _, v := range vs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.At.Local().Format(time.DateTime), why(v.Reason), size(v.Size), v.Path, v.ID)
	}
	return w.Flush()
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	id := fs.String("library", "", "library id")
	path := fs.String("path", "", "the file, relative to the library or absolute")
	version := fs.String("version", "", "the version to restore, from synk versions; without it, the newest")
	_ = fs.Parse(args)
	if *id == "" || *path == "" {
		return fmt.Errorf("restore: --library and --path are required")
	}
	t, err := connect("")
	if err != nil {
		return err
	}
	defer t.close()
	rel, err := t.inLibrary(*id, *path)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("restore: %s is the library's folder; --path names a file in it", *path)
	}
	var ver vault.Version
	if t.daemon != nil {
		ver, err = t.daemon.RestoreVersion(*id, rel, *version)
	} else {
		ver, err = t.node.RestoreVersion(*id, rel, *version)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Restored %s as it was before it was %s, at %s.\n", ver.Path, why(ver.Reason), ver.At.Local().Format(time.DateTime))
	if t.daemon == nil {
		fmt.Println("Paired devices get it once synk serve runs.")
	}
	return nil
}

// inLibrary turns path, relative to library id or absolute, into the path
// in the library that Synk uses; the library's own folder is "".
func (t *target) inLibrary(id, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return slashed(path), nil
	}
	var root string
	if t.daemon != nil {
		st, err := t.daemon.Status()
		if err != nil {
			return "", err
		}
		for _, l := range st.Libraries {
			if l.ID == id {
				root = l.Path
			}
		}
	} else if l, ok := t.node.Library(id); ok {
		root = l.Path
	}
	if root == "" {
		return "", fmt.Errorf("%w %q", engine.ErrUnknownLibrary, id)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		// Either may go through a symlink, as /tmp does on macOS.
		rel, err = filepath.Rel(safepath.Resolve(root), safepath.Resolve(path))
	}
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s isn't in library %s, which is at %s", path, id, root)
	}
	return slashed(rel), nil
}

func slashed(rel string) string {
	if rel = filepath.Clean(rel); rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// why says what happened to a file that made Synk keep a version of it.
func why(reason string) string {
	switch reason {
	case "pre-overwrite":
		return "replaced"
	case "pre-delete":
		return "deleted"
	}
	return reason
}

// size says how big n bytes are, in the decimal units Finder uses.
func size(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	f, unit := float64(n)/1000, 0
	for ; f >= 999.95 && unit < 5; unit++ {
		f /= 1000
	}
	return fmt.Sprintf("%.1f %cB", f, "KMGTPE"[unit])
}
