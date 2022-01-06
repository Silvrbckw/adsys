package privilege

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ubuntu/adsys/internal/consts"
	"github.com/ubuntu/adsys/internal/decorate"
	log "github.com/ubuntu/adsys/internal/grpc/logstreamer"
	"github.com/ubuntu/adsys/internal/i18n"
	"github.com/ubuntu/adsys/internal/policies/entry"
	"gopkg.in/ini.v1"
)

/*
	Notes:
	privilege allows and deny privilege escalation on the client.

	It does so in modifying policykit and sudo files to override default distribution rules.

	This is all or nothing, similarly to the sudo policy files in most default distribution setup.

	We are modifying 2 files:
	- one for sudo, named 99-adsys-privilege-enforcement in sudoers.d
	- one under 99-adsys-privilege-enforcement.conf for policykit

	Both are installed under respective /etc directories.
*/

const adsysBaseConfName = "99-adsys-privilege-enforcement"

// Manager prevents running multiple privilege update process in parallel while parsing policy in ApplyPolicy.
type Manager struct {
	privilegeMu sync.Mutex

	sudoersDir   string
	policyKitDir string
}

// NewWithDirs creates a manager with a specific root directory.
func NewWithDirs(sudoersDir, policyKitDir string) *Manager {
	return &Manager{
		sudoersDir:   sudoersDir,
		policyKitDir: policyKitDir,
	}
}

// ApplyPolicy generates a privilege policy based on a list of entries.
func (m *Manager) ApplyPolicy(ctx context.Context, objectName string, isComputer bool, entries []entry.Entry) (err error) {
	defer decorate.OnError(&err, i18n.G("can't apply privilege policy to %s"), objectName)

	// We only have privilege escalation on computers.
	if !isComputer {
		return nil
	}

	sudoersDir := m.sudoersDir
	if sudoersDir == "" {
		sudoersDir = consts.DefaultSudoersDir
	}
	policyKitDir := m.policyKitDir
	if policyKitDir == "" {
		policyKitDir = consts.DefaultPolicyKitDir
	}
	sudoersConf := filepath.Join(sudoersDir, adsysBaseConfName)
	policyKitConf := filepath.Join(policyKitDir, "localauthority.conf.d", adsysBaseConfName+".conf")

	m.privilegeMu.Lock()
	defer m.privilegeMu.Unlock()

	log.Debugf(ctx, "Applying privilege policy to %s", objectName)

	// We don’t create empty files if there is no entries. Still remove any previous version.
	if len(entries) == 0 {
		if err := os.Remove(sudoersConf); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(policyKitConf); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	// Create our temp files and parent directories
	// nolint:gosec // G301 match distribution permission
	if err := os.MkdirAll(filepath.Dir(sudoersConf), 0755); err != nil {
		return err
	}
	sudoersF, err := os.OpenFile(sudoersConf+".new", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0440)
	if err != nil {
		return err
	}
	defer sudoersF.Close()
	// nolint:gosec // G301 match distribution permission
	if err := os.MkdirAll(filepath.Dir(policyKitConf), 0755); err != nil {
		return err
	}
	// nolint:gosec // G301 match distribution permission
	policyKitConfF, err := os.OpenFile(policyKitConf+".new", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer policyKitConfF.Close()

	systemPolkitAdmins, err := getSystemPolkitAdminIdentities(ctx, policyKitDir)
	if err != nil {
		return err
	}

	// Parse our rules and write to temp files
	var headerWritten bool
	header := `# This file is managed by adsys.
# Do not edit this file manually.
# Any changes will be overwritten.

`

	allowLocalAdmins := true
	var polkitAdditionalUsersGroups []string

	for _, entry := range entries {
		var contentSudo string

		if !headerWritten {
			contentSudo = header
		}

		switch entry.Key {
		case "allow-local-admins":
			allowLocalAdmins = !entry.Disabled
			if allowLocalAdmins {
				continue
			}
			contentSudo += "%admin	ALL=(ALL) !ALL\n"
			contentSudo += "%sudo	ALL=(ALL:ALL) !ALL\n"
		case "client-admins":
			if entry.Disabled {
				continue
			}

			var polkitElem []string
			for _, e := range splitAndNormalizeUsersAndGroups(ctx, entry.Value) {
				contentSudo += fmt.Sprintf("\"%s\"	ALL=(ALL:ALL) ALL\n", e)
				polkitID := fmt.Sprintf("unix-user:%s", e)
				if strings.HasPrefix(e, "%") {
					polkitID = fmt.Sprintf("unix-group:%s", strings.TrimPrefix(e, "%"))
				}
				polkitElem = append(polkitElem, polkitID)
			}
			if len(polkitElem) < 1 {
				continue
			}
			polkitAdditionalUsersGroups = polkitElem
		}

		// Write to our files
		if _, err := sudoersF.WriteString(contentSudo + "\n"); err != nil {
			return err
		}
		headerWritten = true
	}
	// PolicyKitConf files depends on multiple keys, so we need to write it at the end
	if !allowLocalAdmins || polkitAdditionalUsersGroups != nil {
		users := strings.Join(polkitAdditionalUsersGroups, ";")
		// We need to set system local admin here as we override the key from the previous file
		// otherwise, they will be disabled.
		if allowLocalAdmins {
			if systemPolkitAdmins != "" {
				systemPolkitAdmins += ";"
			}
			users = systemPolkitAdmins + users
		}

		if _, err := policyKitConfF.WriteString(fmt.Sprintf("%s[Configuration]\nAdminIdentities=%s", header, users) + "\n"); err != nil {
			return err
		}
	}

	// Move temp files to their final destination
	if err := os.Rename(sudoersConf+".new", sudoersConf); err != nil {
		return err
	}
	if err := os.Rename(policyKitConf+".new", policyKitConf); err != nil {
		return err
	}

	return nil
}

// splitAndNormalizeUsersAndGroups allow splitting on lines and ,.
// We remove any invalid characters and empty elements.
// All will have the form of user@domain.
func splitAndNormalizeUsersAndGroups(ctx context.Context, v string) []string {
	var elems []string
	elems = append(elems, strings.Split(v, "\n")...)
	v = strings.Join(elems, ",")
	elems = nil
	for _, e := range strings.Split(v, ",") {
		initialValue := e
		// Invalid chars in Windows user names: '/[]:|<>+=;,?*%"
		isgroup := strings.HasPrefix(e, "%")
		for _, c := range []string{"/", "[", "]", ":", "|", "<", ">", "=", ";", "?", "*", "%"} {
			e = strings.ReplaceAll(e, c, "")
		}
		if isgroup {
			e = "%" + e
		}

		// domain\user becomes user@domain
		ud := strings.SplitN(e, `\`, 2)
		if len(ud) == 2 {
			e = fmt.Sprintf("%s@%s", ud[1], ud[0])
			e = strings.ReplaceAll(e, `\`, "")
		}

		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if e != initialValue {
			log.Warningf(ctx, "Changed user or group %q to %q: Invalid characters or domain\\user format", initialValue, e)
		}
		elems = append(elems, e)
	}

	return elems
}

// getSystemPolkitAdminIdentities returns the list of configured system polkit admins as a string.
// It lists /etc/polkit-1/localauthority.conf.d and take the highest file in ascii order to match
// from the [configuration] section AdminIdentities value.
func getSystemPolkitAdminIdentities(ctx context.Context, policyKitDir string) (adminIdentities string, err error) {
	defer decorate.OnError(&err, i18n.G("can't get existing system polkit administrators in %s"), policyKitDir)

	polkitConfFiles, err := filepath.Glob(filepath.Join(policyKitDir, "localauthority.conf.d", "*.conf"))
	if err != nil {
		return "", err
	}
	sort.Strings(polkitConfFiles)
	for _, p := range polkitConfFiles {
		fi, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if fi.IsDir() {
			log.Warningf(ctx, i18n.G("%s is a directory. Ignoring."), p)
			continue
		}

		// Ignore ourself
		if filepath.Base(p) == adsysBaseConfName+".conf" {
			continue
		}

		cfg, err := ini.LoadSources(ini.LoadOptions{IgnoreInlineComment: true}, p)
		if err != nil {
			return "", err
		}

		adminIdentities = cfg.Section("Configuration").Key("AdminIdentities").String()
	}

	return adminIdentities, nil
}
