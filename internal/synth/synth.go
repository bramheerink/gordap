// Package synth generates deterministic synthetic RDAP records.
//
// Both the seeder (cmd/gordap-seed) and the stress runner
// (cmd/gordap-stress) draw names from the same generators, so the
// stress runner can predict which queries should succeed and which
// should fail — the basis for inline correctness validation.
//
// Determinism rules:
//   - Every function takes an integer index and returns the same value
//     every time. No randomness, no time-based seeds.
//   - Adding new generators must not shift the output of existing ones.
//   - The TLD/country mixes are stable across releases.
package synth

import "fmt"

// TLDs is the rotation used for domain LDH names. Stable order.
var TLDs = []string{"nl", "com", "de", "test"}

// Countries cycle for entity addresses.
var Countries = []string{"NL", "DE", "BE", "FR", "GB", "US"}

// Kinds picks an entity kind for index i: 60% individual, 40% org.
func Kinds(i int) string {
	if i%5 < 3 {
		return "individual"
	}
	return "org"
}

// DomainName returns the LDH form for the i'th synthetic domain.
func DomainName(i int) string {
	return fmt.Sprintf("syn-%d.%s", i, TLDs[i%len(TLDs)])
}

// DomainHandle returns the EPP-ROID-shaped handle used in storage.
func DomainHandle(i int) string {
	return fmt.Sprintf("SYN-DOM-%d", i)
}

// EntityHandle returns the i'th entity handle.
func EntityHandle(i int) string {
	return fmt.Sprintf("SYN-ENT-%d", i)
}

// NameserverName returns the i'th nameserver LDH name.
func NameserverName(i int) string {
	return fmt.Sprintf("ns%d.synthetic.example", i)
}

// NameserverHandle returns the i'th nameserver handle.
func NameserverHandle(i int) string {
	return fmt.Sprintf("SYN-NS-%d", i)
}

// EntityFullName composes a stable name from two cycling lists.
func EntityFullName(i int) string {
	first := []string{"Alice", "Bob", "Carol", "Dave", "Eve", "Frank", "Grace", "Hank"}[i%8]
	last := []string{"Smith", "Doe", "Brown", "Jones", "Garcia", "Lee", "Patel", "Kim"}[(i/8)%8]
	return fmt.Sprintf("%s %s", first, last)
}

// EntityOrganization clusters every 10 entities under one org so search
// patterns aren't trivially uniform.
func EntityOrganization(i int) string {
	return fmt.Sprintf("Synthetic Org %d B.V.", i/10)
}

// EntityEmail returns one stable email per entity. Multi-email
// scenarios for stress tests can use EntityEmail(i)+"|"+strconv.Itoa(j).
func EntityEmail(i int) string {
	return fmt.Sprintf("contact-%d@synthetic.example", i)
}

// EntityPhone is in E.164 form to exercise the JSContact phone mapping.
func EntityPhone(i int) string {
	return fmt.Sprintf("+312000%05d", i%100000)
}

// EntityCountry rotates through the Countries list.
func EntityCountry(i int) string {
	return Countries[i%len(Countries)]
}

// EntityStreet keeps street numbers low so the address rendering stays
// realistic at any N.
func EntityStreet(i int) string {
	return fmt.Sprintf("Synthetic Street %d", (i%9999)+1)
}

// EntityLocality cycles through a small set of city names.
func EntityLocality(i int) string {
	return []string{"Amsterdam", "Berlin", "Brussels", "Paris", "London", "New York"}[i%6]
}

// EntityPostalCode is freeform; just stable.
func EntityPostalCode(i int) string {
	return fmt.Sprintf("%05d", (i*73)%99999)
}

// NameserverForDomain returns the index of the primary nameserver for
// domain index i. Ten domains share each nameserver, so populate ~N/10
// nameservers when seeding.
func NameserverForDomain(i int) int {
	return i / 10
}

// IDNFixtures is a small fixed pool of synthetic IDN domains used by
// the seeders to exercise the storage round-trip for non-ASCII names.
// Real punycode pairs from RFC 3492 / IDN test vectors; both LDH and
// Unicode forms are valid so idn.Normalize round-trips them.
var IDNFixtures = []struct {
	Handle, LDH, Unicode string
}{
	{"SYN-IDN-1", "xn--bcher-kva.example", "bücher.example"},
	{"SYN-IDN-2", "xn--mller-kva.example", "müller.example"},
	{"SYN-IDN-3", "xn--hxajbheg2az3al.example", "παράδειγμα.example"},
	{"SYN-IDN-4", "xn--fiqs8s.example", "中国.example"},
	{"SYN-IDN-5", "xn--mxaa.example", "ñ.example"},
}

// EntityForDomain returns the index of the registrant for domain i.
// Identity mapping is fine — it gives one entity per domain in the
// dense case but search patterns still vary by org clustering.
func EntityForDomain(i int) int {
	return i
}
