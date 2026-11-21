package hostsetup

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// ParseAccounts reads /etc/passwd or /etc/group: name:password:id:... . Lines that are not that shape are
// skipped. Only plain files are read, so LDAP and other NSS sources are not seen, and the plan says so.
func ParseAccounts(r io.Reader) ([]Account, error) {
	var accounts []Account
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		id, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			continue
		}
		accounts = append(accounts, Account{Name: fields[0], ID: uint32(id)})
	}
	return accounts, scanner.Err()
}
