package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// appURL serves the mutation-test-report-app web component, which renders
// a Stryker report; the same URL Stryker's own HTML reporter embeds.
const appURL = "https://www.unpkg.com/mutation-testing-elements"

// htmlTemplate is the single-file view: the report as embedded JSON plus
// the web component that renders it. The JSON sits in a
// type="application/json" block, where nothing but "</script>" can end it
// early, and the encoder escapes "<" as "<", so it cannot.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>mutrim mutation report</title>
<script src="%s"></script>
</head>
<body>
<mutation-test-report-app title-postfix="mutrim">
This report needs a browser with custom elements and JavaScript.
</mutation-test-report-app>
<script type="application/json" id="mutation-report">%s</script>
<script>
document.querySelector('mutation-test-report-app').report =
  JSON.parse(document.getElementById('mutation-report').textContent);
</script>
</body>
</html>
`

// WriteHTML writes the single-file HTML view of s.
func WriteHTML(w io.Writer, s *Stryker) error {
	data, err := json.Marshal(s) // escaping HTML, unlike the JSON output
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, htmlTemplate, appURL, data)
	return err
}
