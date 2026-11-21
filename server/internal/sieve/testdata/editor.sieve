# Postwerk-Filterregeln – erzeugt vom Regel-Editor der Weboberfläche.
# Manuelle Änderungen kann der Regel-Editor nicht darstellen.
# rules: {"v":1,"rules":[{"name":"Rechnungen \"wichtig\"","enabled":true,"match":"all","conditions":[{"field":"subject","op":"contains","value":"Rechnung"},{"field":"from","op":"is","value":"buchhaltung@firma.de"},{"field":"to","op":"startsWith","value":"a*b\\c"},{"field":"header","header":"List-Id","op":"notContains","value":"x"},{"field":"size","op":"over","value":"500"}],"actions":[{"type":"stop"},{"type":"fileinto","mailbox":"Rechnungen/2026"},{"type":"seen"},{"type":"flag"}]},{"name":"","enabled":true,"match":"any","conditions":[],"actions":[{"type":"redirect","address":"x@y.de","keepCopy":true},{"type":"discard"}]},{"name":"","enabled":false,"match":"all","conditions":[{"field":"subject","op":"contains","value":""}],"actions":[{"type":"redirect","address":"z@y.de"}]}]}
require ["fileinto", "imap4flags", "copy"];

# Regel 1: Rechnungen "wichtig"
if allof (header :contains "subject" "Rechnung", address :is "from" "buchhaltung@firma.de", address :matches ["to", "cc"] "a\\*b\\\\c*", not header :contains "list-id" "x", size :over 500K) {
  addflag "\\Seen";
  addflag "\\Flagged";
  fileinto "Rechnungen/2026";
  stop;
}

# Regel 2
if true {
  redirect :copy "x@y.de";
  discard;
}
