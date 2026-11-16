package engine

import (
	"errors"
	"fmt"
	"slices"
)

// ErrUnknownPeer means no paired device has the ID.
var ErrUnknownPeer = errors.New("engine: unknown device")

// PauseLibrary stops syncing a library until ResumeLibrary. Its files
// stay as they are, and a peer that asks to sync it is refused.
func (n *Node) PauseLibrary(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	l := n.Config.LibraryByID(id)
	if l == nil {
		return fmt.Errorf("%w %q", ErrUnknownLibrary, id)
	}
	l.Paused = true
	if err := n.saveConfigLocked(); err != nil {
		return err
	}
	n.Log.Info("paused a library", "library", id)
	n.publish("library", id, "", "info", "paused; it won't sync until you resume it")
	return nil
}

// ResumeLibrary starts syncing a paused library again. Everything that
// changed while it was paused syncs at the next session.
func (n *Node) ResumeLibrary(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	l := n.Config.LibraryByID(id)
	if l == nil {
		return fmt.Errorf("%w %q", ErrUnknownLibrary, id)
	}
	l.Paused = false
	if err := n.saveConfigLocked(); err != nil {
		return err
	}
	n.Log.Info("resumed a library", "library", id)
	n.publish("library", id, "", "info", "resumed; what changed while it was paused syncs now")
	return nil
}

// RemoveLibrary forgets a library: it stops syncing, and Synk keeps
// every file, the library's .synk marker, the versions in the vault,
// and the index and cache on disk, so enabling the folder again picks
// up where it left off. It deletes nothing in the library.
func (n *Node) RemoveLibrary(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Config.LibraryByID(id) == nil {
		return fmt.Errorf("%w %q", ErrUnknownLibrary, id)
	}
	n.Config.RemoveLibrary(id)
	delete(n.indexes, id)
	delete(n.caches, id)
	delete(n.saved, id)
	delete(n.gens, id)
	delete(n.scans, id)
	delete(n.held, id)
	delete(n.confirmed, id)
	delete(n.scanErrs, id)
	delete(n.folds, id)
	delete(n.recovered, id)
	for p := range n.synced {
		if p.library == id {
			delete(n.synced, p)
		}
	}
	if err := n.saveConfigLocked(); err != nil {
		return err
	}
	n.Log.Info("removed a library; its files and versions are kept", "library", id)
	n.publish("library", id, "", "info", "removed from Synk; its files and versions are kept")
	return nil
}

// Unpair removes a paired device: this device stops syncing every
// library with it, and the libraries stop sharing with it. Nothing on
// disk changes, and the device can pair again with a new offer.
func (n *Node) Unpair(deviceID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Config.PeerByID(deviceID) == nil {
		return fmt.Errorf("%w %s", ErrUnknownPeer, deviceID)
	}
	name := deviceID
	if p := n.Config.PeerByID(deviceID); p != nil {
		name = p.Name
	}
	n.Config.RemovePeer(deviceID)
	for i := range n.Config.Libraries {
		l := &n.Config.Libraries[i]
		l.ShareWith = slices.DeleteFunc(l.ShareWith, func(id string) bool { return id == deviceID })
	}
	for p := range n.synced {
		if p.device == deviceID {
			delete(n.synced, p)
		}
	}
	if err := n.saveConfigLocked(); err != nil {
		return err
	}
	n.Log.Info("unpaired a device", "device", deviceID)
	n.publish("peer", "", deviceID, "info", name+" is unpaired; it can pair again with a new offer")
	return nil
}
