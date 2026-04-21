package mapper

import (
	"github.com/bramheerink/gordap/pkg/rdap/auth"
	"github.com/bramheerink/gordap/pkg/rdap/datasource"
	"github.com/bramheerink/gordap/pkg/rdap/types"
)

// ContactView is the post-redaction projection of a Contact. It is the
// exported result of RedactContact so external callers can reuse the
// same tiering without having to rebuild the policy.
type ContactView struct {
	FullName     string
	Organization string
	Title        string
	Emails       []string
	Phones       []datasource.Phone
	Address      *datasource.Address

	// Marks records which field-kinds were stripped from this contact.
	// The mapper translates these into RFC 9537 Redaction entries on
	// the top-level response. Duplicates across multiple contacts are
	// de-duplicated at aggregation time.
	Marks []RedactionKind
}

// RedactionKind enumerates the field-types the simple policy redacts.
// Values correspond to JSONPath targets in kindPath() below.
type RedactionKind int

const (
	RedactName RedactionKind = iota + 1
	RedactTitle
	RedactEmail
	RedactPhone
	RedactPostalAddress
)

// kindDescriptions and kindPaths are parallel lookup tables the RFC 9537
// emitter consults. Splitting them keeps the RedactionKind enum cheap.
var (
	kindDescriptions = map[RedactionKind]string{
		RedactName:          "Contact full name",
		RedactTitle:         "Contact title",
		RedactEmail:         "Contact email address",
		RedactPhone:         "Contact phone number",
		RedactPostalAddress: "Contact postal address",
	}
	kindPaths = map[RedactionKind]string{
		RedactName:          "$.entities[*].jscard.name.full",
		RedactTitle:         "$.entities[*].jscard.titles",
		RedactEmail:         "$.entities[*].jscard.emails",
		RedactPhone:         "$.entities[*].jscard.phones",
		RedactPostalAddress: "$.entities[*].jscard.addresses",
	}
)

// Empty reports whether the view carries any caller-visible data.
func (v ContactView) Empty() bool {
	return v.FullName == "" && v.Organization == "" && v.Title == "" &&
		len(v.Emails) == 0 && len(v.Phones) == 0 && v.Address == nil
}

// RedactContact applies the GDPR tiering. The policy here is
// intentionally conservative and matches common European ccTLD practice:
//
//	Anonymous      → organisation name only (natural persons fully
//	                 hidden); country code retained.
//	Authenticated  → + technical contact channels (email, abuse phone);
//	                 no postal address for natural persons.
//	Privileged     → full record.
//
// Marks record what was removed so the mapper can emit RFC 9537
// signals on the top-level response.
func RedactContact(c datasource.Contact, lvl auth.AccessLevel) ContactView {
	isOrg := c.Kind == "org" || c.Kind == "organization"

	switch lvl {
	case auth.AccessPrivileged:
		return ContactView{
			FullName:     c.FullName,
			Organization: c.Organization,
			Title:        c.Title,
			Emails:       c.Emails,
			Phones:       c.Phones,
			Address:      c.Address,
		}

	case auth.AccessAuthenticated:
		v := ContactView{
			Organization: c.Organization,
			Emails:       c.Emails,
			Phones:       c.Phones,
		}
		if isOrg {
			v.FullName = c.FullName
			v.Address = c.Address
		} else {
			if c.FullName != "" {
				v.Marks = append(v.Marks, RedactName)
			}
			if c.Title != "" {
				v.Marks = append(v.Marks, RedactTitle)
			}
			if c.Address != nil {
				// Keep country only; strip street/city/postcode.
				v.Address = &datasource.Address{CountryCode: c.Address.CountryCode}
				v.Marks = append(v.Marks, RedactPostalAddress)
			}
		}
		return v

	default: // AccessAnonymous
		v := ContactView{Organization: c.Organization}
		if isOrg {
			v.Address = c.Address
		} else {
			if c.FullName != "" {
				v.Marks = append(v.Marks, RedactName)
			}
			if c.Title != "" {
				v.Marks = append(v.Marks, RedactTitle)
			}
			if len(c.Emails) > 0 {
				v.Marks = append(v.Marks, RedactEmail)
			}
			if len(c.Phones) > 0 {
				v.Marks = append(v.Marks, RedactPhone)
			}
			if c.Address != nil {
				v.Address = &datasource.Address{CountryCode: c.Address.CountryCode}
				v.Marks = append(v.Marks, RedactPostalAddress)
			}
		}
		return v
	}
}

// buildRedacted converts accumulated kind marks into the RFC 9537
// top-level redacted array. Duplicates are collapsed so a domain with
// three individuals whose emails were redacted emits one marker, not
// three.
func buildRedacted(marks []RedactionKind, reason string) []types.Redaction {
	if len(marks) == 0 {
		return nil
	}
	seen := map[RedactionKind]bool{}
	out := make([]types.Redaction, 0, len(marks))
	for _, k := range marks {
		if seen[k] {
			continue
		}
		seen[k] = true
		r := types.Redaction{
			Name:     types.RedactionLabel{Description: kindDescriptions[k]},
			PrePath:  kindPaths[k],
			PathLang: "jsonpath",
			Method:   "removal",
		}
		if reason != "" {
			r.Reason = &types.RedactionLabel{Description: reason}
		}
		out = append(out, r)
	}
	return out
}
