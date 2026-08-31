package run

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/atyrode/babel/internal/worker"
)

// redactedMarker replaces a credential-shaped value. It is deliberately
// visible: a reviewer should see that something was removed here, because a
// silently cleaned receipt hides the fact that the other side of the worker
// boundary tried to write a credential into Babel's durable record.
const redactedMarker = "[redacted]"

// credentialKeyMarkers make a map key credential-shaped. The list mirrors
// internal/worker's metadata check rather than inventing a second vocabulary,
// and adds "dsn" and "passphrase" because SPEC.md §9 forbids database URLs
// from reaching a log or a diagnostic. Substring matching is deliberate, for
// the same reason it is there: the rule must not be defeated by naming a field
// "openai_api_key_value".
//
// The markers are narrow on purpose. A broader "key" would wipe a legitimate
// "public_key_fingerprint", and a receipt that quietly loses non-secret
// provenance is its own kind of failure.
var credentialKeyMarkers = []string{
	"api_key", "apikey", "authorization", "bearer", "credential", "dsn",
	"passphrase", "passwd", "password", "private_key", "secret", "token",
}

// credentialPatterns are the credential shapes this package removes from every
// worker-controlled string before it can reach storage, a log line or an
// error.
//
// The set is deliberately small and deterministic rather than a general secret
// scanner. SPEC.md §6.4 owns likely-secret detection over corpus material;
// what is needed here is narrower and absolute — nothing that looks like a
// credential may enter an immutable audit record — so the patterns cover the
// shapes a misbehaving counterpart or a copied command line actually produces:
// a named credential field, an AWS-style key id, a PEM private key block, and
// credentials embedded in a URL's userinfo (SPEC.md §9 forbids DSNs in logs).
//
// Each pattern keeps its match's non-secret prefix and replaces only the value,
// so redacting inside a JSON payload leaves the JSON well-formed: the value
// character class stops at a quote, comma, semicolon or closing bracket.
var credentialPatterns = []*regexp.Regexp{
	// PEM private key blocks, header through footer.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	// Credentials in a URL's userinfo: scheme://user:secret@host.
	regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^\s/:@"']+:[^\s/@"']+@`),
	// A named credential field followed by its value.
	regexp.MustCompile(`(?i)((?:api[_-]?key|secret[_-]?access[_-]?key|access[_-]?key[_-]?id|secret[_-]?key|client[_-]?secret|auth[_-]?token|access[_-]?token|refresh[_-]?token|bearer|password|passwd|passphrase|credential|authorization|secret|token)["']?\s*[:=]\s*["']?)[^\s"',;)\]}]+`),
	// AWS-style key identifiers, which carry no field name of their own.
	regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ASCA)[0-9A-Z]{16}\b`),
}

// credentialReplacements are the templates paired with credentialPatterns. A
// pattern with a capture group keeps group 1 and drops the rest.
var credentialReplacements = []string{
	redactedMarker,
	"${1}" + redactedMarker + "@",
	"${1}" + redactedMarker,
	redactedMarker,
}

// redactString removes every credential shape from s, reporting how many were
// removed.
//
// The common case is no match at all, and it costs one scan per pattern with
// no allocation; only a string that actually matches pays for counting and
// rewriting.
func redactString(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	total := 0
	for i, re := range credentialPatterns {
		if !re.MatchString(s) {
			continue
		}
		total += len(re.FindAllStringIndex(s, -1))
		s = re.ReplaceAllString(s, credentialReplacements[i])
	}
	return s, total
}

// redactValueFor removes a value whose key names a credential outright, and
// otherwise applies the pattern set. A key called "token" says what its value
// is; waiting for the value to look like a credential would be a worse rule
// than believing the field name.
func redactValueFor(key, value string) (string, int) {
	lower := strings.ToLower(key)
	for _, marker := range credentialKeyMarkers {
		if strings.Contains(lower, marker) {
			if value == redactedMarker || value == "" {
				return value, 0
			}
			return redactedMarker, 1
		}
	}
	return redactString(value)
}

// redactJSON removes credential shapes from a structured payload.
//
// The patterns are written so a replacement cannot straddle a JSON string's
// quotes, but a payload is worker-controlled and this is the one place where
// being wrong would corrupt a record rather than merely fail. So the result is
// re-validated, and a payload that redaction somehow broke is dropped whole
// rather than stored as unparseable bytes.
func redactJSON(raw json.RawMessage) (json.RawMessage, int) {
	if len(raw) == 0 {
		return nil, 0
	}
	cleaned, n := redactString(string(raw))
	if n == 0 {
		out := make(json.RawMessage, len(raw))
		copy(out, raw)
		return out, 0
	}
	if !json.Valid([]byte(cleaned)) {
		return json.RawMessage("null"), n
	}
	return json.RawMessage(cleaned), n
}

// redactBody deep-copies a receipt body and removes credential shapes from
// every worker-controlled string in it, reporting the count.
//
// Two jobs in one pass, because they have the same reason. The copy means a
// caller that keeps mutating its slices after handing them over cannot change
// a receipt that is supposed to be immutable; the redaction means nothing a
// counterpart wrote can reach storage or an error string.
//
// The preparation record is deliberately not redacted. It is derived from
// Babel's own discovery of its own filesystem, it is not a channel a
// counterpart can write to, and its content is hashed into the preparation ID
// — rewriting a selector would silently change the scope's identity.
func redactBody(b Body) (Body, int) {
	total := 0
	out := b

	out.Cookbook = append([]CookbookAsset(nil), b.Cookbook...)
	out.Frontier = FrontierScope{
		Roots: append([]string(nil), b.Frontier.Roots...),
		Prior: append([]string(nil), b.Frontier.Prior...),
	}
	out.AmendmentReason, total = addRedaction(total, b.AmendmentReason)

	out.Retrieval = make([]RetrievalStep, len(b.Retrieval))
	for i, step := range b.Retrieval {
		step.Query, total = addRedaction(total, step.Query)
		results := make([]RetrievalResult, len(step.Results))
		for j, hit := range step.Results {
			hit.Evidence.note, total = addRedaction(total, hit.Evidence.note)
			results[j] = hit
		}
		step.Results = results
		// The research record is copied through the pointer rather than
		// past it, so a caller that keeps its Document cannot edit a
		// stored receipt afterwards. Its strings are deliberately not
		// redacted: the URL and its redirect chain are Babel's own record
		// of an operator-fixed source that was refused at configuration
		// time if it carried userinfo, so there is no credential shape to
		// find and rewriting one would corrupt the locator a reviewer
		// re-fetches.
		if step.Research != nil {
			source := *step.Research
			source.Redirects = append([]string(nil), source.Redirects...)
			step.Research = &source
		}
		out.Retrieval[i] = step
	}
	if len(out.Retrieval) == 0 {
		out.Retrieval = nil
	}

	out.Deferred, total = redactCandidates(b.Deferred, total)
	out.Rejected, total = redactCandidates(b.Rejected, total)

	out.Failures = make([]Failure, len(b.Failures))
	for i, f := range b.Failures {
		f.Message, total = addRedaction(total, f.Message)
		out.Failures[i] = f
	}
	if len(out.Failures) == 0 {
		out.Failures = nil
	}

	out.Resources = copyResources(b.Resources)
	out.Worker, total = redactWorkerReceipt(b.Worker, total)
	return out, total
}

// addRedaction is the accumulate-and-clean step every string in the body goes
// through, so no call site can clean a string and forget to count it.
func addRedaction(total int, s string) (string, int) {
	cleaned, n := redactString(s)
	return cleaned, total + n
}

func redactCandidates(in []Candidate, total int) ([]Candidate, int) {
	if len(in) == 0 {
		return nil, total
	}
	out := make([]Candidate, len(in))
	for i, c := range in {
		c.Reason, total = addRedaction(total, c.Reason)
		origin := make([]Evidence, len(c.Origin))
		for j, e := range c.Origin {
			e.note, total = addRedaction(total, e.note)
			origin[j] = e
		}
		if len(origin) == 0 {
			origin = nil
		}
		c.Origin = origin
		out[i] = c
	}
	return out, total
}

// copyResources copies the measurements rather than the pointers, so a caller
// that reuses its own variables cannot change a stored measurement afterwards.
// A nil field stays nil: absence is the record, not a value to be copied.
func copyResources(r Resources) Resources {
	var out Resources
	if r.CPUSeconds != nil {
		out.CPUSeconds = new(*r.CPUSeconds)
	}
	if r.MaxRSSBytes != nil {
		out.MaxRSSBytes = new(*r.MaxRSSBytes)
	}
	if r.SandboxBytesWritten != nil {
		out.SandboxBytesWritten = new(*r.SandboxBytesWritten)
	}
	if r.ToolCalls != nil {
		out.ToolCalls = new(*r.ToolCalls)
	}
	return out
}

// redactWorkerReceipt deep-copies the embedded worker receipt and cleans the
// strings the counterpart controls.
//
// internal/worker already scrubs the run-scoped broker token it issued and
// records tool arguments as a digest rather than as content, so the remaining
// exposure is a value the worker itself composed: a metadata value, a
// diagnostic tail, a denial reason it echoed, a progress message, its
// structured result, or its own failure text. Identifiers Babel assigned —
// the job and run ids, the profile reference, the source selectors it echoed
// back — are left alone, because rewriting them would break the correlation
// the receipt exists to support.
func redactWorkerReceipt(in *worker.Receipt, total int) (*worker.Receipt, int) {
	if in == nil {
		return nil, total
	}
	out := *in

	out.Recipes = append([]worker.RecipeRef(nil), in.Recipes...)
	out.Sources = append([]worker.Source(nil), in.Sources...)
	out.ResolvedCapabilities = append([]worker.Capability(nil), in.ResolvedCapabilities...)
	out.UnknownFields = append([]string(nil), in.UnknownFields...)
	out.Grant.Capabilities = append([]worker.Capability(nil), in.Grant.Capabilities...)

	out.Worker.Name, total = addRedaction(total, in.Worker.Name)
	out.Worker.Version, total = addRedaction(total, in.Worker.Version)
	out.StderrTail, total = addRedaction(total, in.StderrTail)

	if in.Metadata != nil {
		metadata := make(map[string]string, len(in.Metadata))
		for k, v := range in.Metadata {
			cleaned, n := redactValueFor(k, v)
			metadata[k] = cleaned
			total += n
		}
		out.Metadata = metadata
	}

	out.ToolRequests = make([]worker.ToolRecord, len(in.ToolRequests))
	for i, rec := range in.ToolRequests {
		rec.Reason, total = addRedaction(total, rec.Reason)
		out.ToolRequests[i] = rec
	}
	if len(out.ToolRequests) == 0 {
		out.ToolRequests = nil
	}

	out.Progress = make([]worker.ProgressRecord, len(in.Progress))
	for i, rec := range in.Progress {
		rec.Message, total = addRedaction(total, rec.Message)
		out.Progress[i] = rec
	}
	if len(out.Progress) == 0 {
		out.Progress = nil
	}

	if in.Result != nil {
		result := *in.Result
		var n int
		result.Payload, n = redactJSON(in.Result.Payload)
		total += n
		out.Result = &result
	}
	if in.Failure != nil {
		failure := *in.Failure
		failure.Message, total = addRedaction(total, in.Failure.Message)
		out.Failure = &failure
	}
	if in.Resources != nil {
		resources := *in.Resources
		out.Resources = &resources
	}
	return &out, total
}
