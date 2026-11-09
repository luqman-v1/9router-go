//go:build !windows && !darwin

package browsercookies

import (
	"context"
	"os/exec"
	"time"

	"github.com/godbus/dbus/v5"
)

// The secret store, read over D-Bus.
//
// Chromium keeps its cookie-encryption secret in the login keyring, under
// the application's own name. Two ways reach it: the `secret-tool` command
// that ships with libsecret, or the secret service's own D-Bus interface.
//
// The D-Bus way is here because the command is not always installed. A
// desktop with the keyring but without libsecret's tools has a keyring that
// works and no way to ask it, and the difference between "the keyring is
// locked" and "the tools are missing" is the difference between a fix the
// operator can make and one they cannot.

// dbusSecretService is the well-known name of the secret service.
const dbusSecretService = "org.freedesktop.secrets"

// dbusSecretObject is the object that carries the service's methods.
const dbusSecretObject = "/org/freedesktop/secrets"

// dbusCollectionPath is the login collection, which is where a browser's
// secret is kept.
const dbusCollectionPath = "/org/freedesktop/secrets/collection/login"

// dbusLookupTimeout bounds the whole lookup.
//
// The keyring may be locked, and unlocking it asks the user for a password.
// A lookup that waits on a prompt the operator cannot see is a lookup that
// hangs, and the router must not hang with it.
const dbusLookupTimeout = 20 * time.Second

// platformKeyringSecret reads a secret out of the login keyring over D-Bus.
//
// The command-line tool is tried first, because it is what a machine that
// has the tools expects to be asked through, and the D-Bus way is the
// fallback for a machine that has the keyring but not the tools.
func platformKeyringSecret(ctx context.Context, app string) string {
	if secret := secretToolLookup(ctx, app); secret != "" {
		return secret
	}
	return dbusKeyringSecret(ctx, app)
}

// secretToolLookup reads a secret with the libsecret command.
func secretToolLookup(ctx context.Context, app string) string {
	ctx, cancel := context.WithTimeout(ctx, dbusLookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "secret-tool", "lookup", "application", app).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// dbusKeyringSecret reads a secret over the secret service's D-Bus
// interface.
//
// The service is asked for a session, the collection is searched for an
// item whose attributes name the application, and the item's secret is
// unsealed. A locked collection answers with an error, which is the same as
// an absent secret as far as the caller is concerned.
func dbusKeyringSecret(ctx context.Context, app string) string {
	ctx, cancel := context.WithTimeout(ctx, dbusLookupTimeout)
	defer cancel()

	// The shared session bus, not a private one: a private connection asks
	// the bus for a name of its own, and that handshake does not come back
	// on every bus, which leaves the lookup hanging.
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()

	secretObject := conn.Object(dbusSecretService, dbus.ObjectPath(dbusSecretObject))

	// A session is needed before an item's secret can be unsealed. The
	// "plain" algorithm sends the secret as it is, over the session bus,
	// which is the same transport the keyring already uses.
	var output dbus.Variant
	var sessionPath dbus.ObjectPath
	call := secretObject.CallWithContext(ctx, "org.freedesktop.Secret.Service.OpenSession",
		0, "plain", dbus.MakeVariant(""))
	if call.Err != nil {
		return ""
	}
	if err := call.Store(&output, &sessionPath); err != nil {
		return ""
	}
	if sessionPath == "" {
		return ""
	}

	items, err := searchItems(ctx, conn, sessionPath, app)
	if err != nil {
		return ""
	}
	for _, item := range items {
		if secret := readItemSecret(ctx, conn, sessionPath, item); secret != "" {
			return secret
		}
	}
	return ""
}

// searchItems lists the items in the login collection that belong to an
// application.
//
// The attributes are what say which item is the browser's, and the browser
// stores its secret under a schema of its own — so an item is matched on
// the application name and on that schema, and not on the label, which the
// user is free to change.
func searchItems(ctx context.Context, conn *dbus.Conn, session dbus.ObjectPath, app string) ([]dbus.ObjectPath, error) {
	collection := conn.Object(dbusSecretService, dbus.ObjectPath(dbusCollectionPath))
	var result []dbus.ObjectPath
	call := collection.CallWithContext(ctx, "org.freedesktop.Secret.Collection.SearchItems", 0, map[string]string{})
	if call.Err != nil {
		return nil, call.Err
	}
	if err := call.Store(&result); err != nil {
		return nil, err
	}

	var matching []dbus.ObjectPath
	for _, item := range result {
		attributes, err := itemAttributes(ctx, conn, item)
		if err != nil {
			continue
		}
		if attributesBelongTo(attributes, app) {
			matching = append(matching, item)
		}
	}
	return matching, nil
}

// itemAttributes reads an item's attributes.
func itemAttributes(ctx context.Context, conn *dbus.Conn, item dbus.ObjectPath) (map[string]string, error) {
	object := conn.Object(dbusSecretService, item)
	var attributes map[string]string
	call := object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0,
		"org.freedesktop.Secret.Item", "Attributes")
	if call.Err != nil {
		return nil, call.Err
	}
	if err := call.Store(&attributes); err != nil {
		return nil, err
	}
	return attributes, nil
}

// attributesBelongTo reports whether an item's attributes name the browser.
//
// A browser stores its secret under the application's own name. The two
// spellings are both in use — the older `application` and the newer
// `app_id` — and a machine may hold either.
func attributesBelongTo(attributes map[string]string, app string) bool {
	for _, key := range []string{"application", "app_id"} {
		if value, ok := attributes[key]; ok && value == app {
			return true
		}
	}
	return false
}

// readItemSecret unseals an item's secret.
func readItemSecret(ctx context.Context, conn *dbus.Conn, session, item dbus.ObjectPath) string {
	object := conn.Object(dbusSecretService, item)
	var secret []interface{}
	call := object.CallWithContext(ctx, "org.freedesktop.Secret.Item.GetSecret", 0, session)
	if call.Err != nil {
		return ""
	}
	if err := call.Store(&secret); err != nil {
		return ""
	}
	if len(secret) < 3 {
		return ""
	}
	bytes, ok := secret[2].([]byte)
	if !ok {
		return ""
	}
	return string(bytes)
}
