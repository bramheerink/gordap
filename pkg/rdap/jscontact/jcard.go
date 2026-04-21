package jscontact

// jCard (RFC 7095) serialisation. ICANN-contracted gTLD operators still
// require the `vcardArray` member on every entity — JSContact is the
// forward-looking format but jCard is what the conformance tool looks
// for today. Emitting both keeps us compatible with clients on either
// side of the migration.
//
// jCard is a compact-serialised vCard 4.0 (RFC 6350) as a two-element
// array: ["vcard", [<property>, ...]]. Each property is itself an
// array: [name, parameters, value_type, value(s)].

// ToJCard converts a minimal JSContact Card into its jCard wire form.
// Unknown / unmapped JSContact members are skipped silently — jCard is
// a lossy projection of JSContact and that's by design of RFC 7095.
//
// The output is structured `any` values because encoding/json already
// knows how to marshal the shapes we produce (strings, maps, nested
// slices). Avoiding a concrete jCard type keeps this a pure data
// function: no schema, no allocation beyond the slices themselves.
func ToJCard(c *Card) []any {
	if c == nil {
		return nil
	}
	props := [][]any{
		{"version", map[string]any{}, "text", "4.0"},
	}
	if c.Name != nil && c.Name.Full != "" {
		props = append(props, []any{"fn", map[string]any{}, "text", c.Name.Full})
	}
	for _, o := range c.Organizations {
		if o.Name != "" {
			props = append(props, []any{"org", map[string]any{}, "text", o.Name})
		}
	}
	for _, t := range c.Titles {
		if t.Name != "" {
			props = append(props, []any{"title", map[string]any{}, "text", t.Name})
		}
	}
	for _, e := range c.Emails {
		if e.Address != "" {
			props = append(props, []any{"email", contextParams(e.Contexts), "text", e.Address})
		}
	}
	for _, p := range c.Phones {
		if p.Number == "" {
			continue
		}
		params := contextParams(p.Contexts)
		// Phone features map to the vCard `type` parameter. Keep the
		// order stable for golden-file testing by iterating a fixed
		// preference list.
		for _, feat := range []string{"voice", "fax", "mobile", "sms", "video"} {
			if p.Features[feat] {
				appendParam(params, "type", feat)
			}
		}
		props = append(props, []any{"tel", params, "uri", "tel:" + p.Number})
	}
	for _, a := range c.Addresses {
		// vCard ADR value is a 7-element array: [pobox, ext, street,
		// locality, region, postcode, country]. JSContact components
		// are an ordered list by kind; we flatten them into the
		// vCard slots.
		pobox, ext, street, locality, region, postcode, country := "", "", "", "", "", "", ""
		for _, comp := range a.Components {
			switch comp.Kind {
			case "name", "number":
				if street == "" {
					street = comp.Value
				} else {
					street += " " + comp.Value
				}
			case "locality":
				locality = comp.Value
			case "region":
				region = comp.Value
			case "postcode":
				postcode = comp.Value
			case "country":
				country = comp.Value
			case "pobox":
				pobox = comp.Value
			}
		}
		if country == "" {
			country = a.CountryCode
		}
		props = append(props, []any{
			"adr", contextParams(a.Contexts), "text",
			[]any{pobox, ext, street, locality, region, postcode, country},
		})
	}

	// Unwrap [][]any to []any for the outer array.
	out := make([]any, len(props))
	for i, p := range props {
		out[i] = p
	}
	return []any{"vcard", out}
}

// contextParams lifts a JSContact contexts map into a vCard-style
// parameter object. vCard convention: `{"type":"work"}` for a single
// context, `{"type":["work","home"]}` when more than one is set.
func contextParams(ctx map[string]bool) map[string]any {
	params := map[string]any{}
	var types []string
	for _, known := range []string{"work", "home", "private"} {
		if ctx[known] {
			types = append(types, known)
		}
	}
	if len(types) == 1 {
		params["type"] = types[0]
	} else if len(types) > 1 {
		params["type"] = types
	}
	return params
}

func appendParam(params map[string]any, key, value string) {
	existing, ok := params[key]
	if !ok {
		params[key] = value
		return
	}
	switch v := existing.(type) {
	case string:
		params[key] = []string{v, value}
	case []string:
		params[key] = append(v, value)
	}
}
