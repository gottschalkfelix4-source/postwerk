# Single Sign-on und LDAP

Postwerk kann Konten aus einem **LDAP-Verzeichnis** anmelden und die Weboberfläche per
**OpenID Connect (OIDC)** an einen Identitätsanbieter wie Authentik oder Keycloak anbinden. Beides wird
unter **Einstellungen → Verzeichnis (LDAP)** bzw. **Einstellungen → Single Sign-on** eingerichtet.

## Grundsätze

- Konten werden immer über die **E-Mail-Adresse** zugeordnet. Die Domain muss in Postwerk angelegt
  sein, sonst wird kein Konto erzeugt.
- **App-Passwörter und die Zwei-Faktor-Anmeldung bleiben in Postwerk.**
- **LDAP-Konten** melden sich in der Weboberfläche und bei IMAP, SMTP, POP3 mit ihrem
  Verzeichnis-Passwort an. Das Passwort wird im Verzeichnis geändert, nicht in Postwerk.
- **SSO-Anmeldungen überspringen Postwerks eigene 2FA** – die MFA übernimmt der Identitätsanbieter.
  Per SSO angelegte Konten haben kein Postwerk-Passwort; Mail-Programme (IMAP, SMTP, CalDAV/CardDAV)
  nutzen **App-Passwörter** (Webmail → Konto & Sicherheit). Neue App-Passwörter lassen sich bis
  15 Minuten nach einer SSO-Anmeldung anlegen.
- **„Nur SSO erlauben“** schaltet die Passwort-Anmeldung der Weboberfläche ab – außer für lokale
  Administratorkonten. So bleibt ein Notzugang, falls der Anbieter ausfällt. Postwerk lässt die Option
  nur zu, wenn mindestens ein aktives lokales Administratorkonto existiert.
- Bestehende lokale Konten können sich zusätzlich per SSO anmelden und behalten ihr Passwort und ihre
  Rolle. Die Admin-Rolle aus dem Admin-Claim gilt nur für Konten, die per SSO angelegt wurden.
- **Lokale Administratorkonten** sind davon ausgenommen: Sie melden sich immer mit Passwort (und
  Zwei-Faktor-Code) an. Wer beim Anbieter die Adresse eines Admins eintragen kann, kommt so nicht an
  dessen Konto.
- Bei der ersten SSO-Anmeldung merkt sich Postwerk die Benutzerkennung des Anbieters (`sub`). Danach
  nimmt das Konto nur noch Anmeldungen dieses Anbieter-Benutzers an, auch wenn ein anderer Benutzer
  später dieselbe E-Mail-Adresse bekommt. Nach einem Wechsel des Anbieters (andere Issuer-URL) wird
  die Verknüpfung bei der nächsten Anmeldung neu gesetzt.
- **Zwei-Faktor-Anmeldung:** Bei einer SSO-Anmeldung fragt Postwerk keinen eigenen 2FA-Code ab – die
  Absicherung liegt dann beim Identitätsanbieter. Schalte dort MFA für alle Benutzer ein, sonst ist ein
  Konto mit Postwerk-2FA per SSO nur so gut geschützt wie sein Anbieter-Passwort. Für sensible Aktionen
  (App-Passwörter, Schlüssel, Backups) verlangt Postwerk eine SSO-Anmeldung, die höchstens 15 Minuten alt ist.

## OIDC: Redirect-URI und Scopes

Die genauen Adressen stehen in den Einstellungen unter „Beim Anbieter eintragen“ (mit Kopier-Button).
Sie werden aus der **öffentlichen URL** (Einstellungen → Web & Proxy) gebildet:

| Feld beim Anbieter | Wert |
|---|---|
| Redirect-URI / Callback | `https://mail.example.org/api/auth/oidc/callback` |
| Post-Logout-Redirect-URI | `https://mail.example.org/login` |
| Client-Typ | vertraulich (confidential), Authorization Code, PKCE S256 |
| Scopes | `openid email profile` (für den Admin-Claim zusätzlich z. B. `groups`) |

Postwerk verlangt `email_verified=true` im Token. Wer das beim Anbieter nicht liefern kann, schaltet
„Unbestätigte E-Mail-Adressen erlauben“ ein – aber nur, wenn Benutzer ihre Adresse dort nicht selbst
ändern können.

### Authentik

1. **Applications → Providers → Create → OAuth2/OpenID Provider**
   - Client type: *Confidential*
   - Redirect URIs: `https://mail.example.org/api/auth/oidc/callback` (strict)
   - Signing Key: ein Zertifikat auswählen (RS256)
   - Scopes: `openid`, `email`, `profile` (Standard-Mappings)
2. **Applications → Create**, Slug z. B. `postwerk`, den Provider zuordnen.
3. In Postwerk eintragen:
   - Issuer-URL: `https://auth.example.org/application/o/postwerk/` (mit abschließendem `/`, genau wie
     unter „OpenID Configuration Issuer“ im Provider angezeigt)
   - Client-ID und Client-Secret aus dem Provider
   - Button: z. B. „Mit Authentik anmelden“
4. Admin-Rolle über Gruppen (optional): Scope `profile` liefert bei Authentik den Claim `groups`.
   Admin-Claim `groups`, Admin-Wert z. B. `mail-admins`.
5. Falls die Anmeldung mit „E-Mail-Adresse nicht bestätigt“ scheitert: Ältere Authentik-Versionen
   senden `email_verified: false`. Dann das Scope-Mapping für `email` anpassen
   (`"email_verified": True`) oder die Prüfung in Postwerk abschalten.
6. Abmelden beim Anbieter: „Auch beim Anbieter abmelden“ einschalten; Authentik nutzt dafür den
   `end_session_endpoint` des Providers.

### Keycloak

1. Im Realm **Clients → Create client**
   - Client type: *OpenID Connect*, Client ID z. B. `postwerk`
   - Client authentication: *On* (vertraulich), Standard flow: *On*, alle anderen Flows aus
   - Valid redirect URIs: `https://mail.example.org/api/auth/oidc/callback`
   - Valid post logout redirect URIs: `https://mail.example.org/login`
   - Unter *Advanced → Proof Key for Code Exchange Code Challenge Method*: `S256`
2. Client-Secret unter **Credentials** kopieren.
3. In Postwerk eintragen:
   - Issuer-URL: `https://kc.example.org/realms/<realm>`
   - Client-ID und Client-Secret
4. Admin-Rolle über Gruppen (optional): Client scope mit Mapper *Group Membership*
   (Token Claim Name `groups`, „Full group path“ aus) anlegen und dem Client zuweisen. In Postwerk
   Scope `groups` ergänzen, Admin-Claim `groups`, Admin-Wert z. B. `mail-admins`.
5. Benutzer brauchen eine bestätigte E-Mail-Adresse (*Email verified* im Benutzerprofil).

## LDAP

| Feld | Beispiel OpenLDAP / lldap | Beispiel Active Directory |
|---|---|---|
| Server-URL | `ldaps://ldap.example.org` | `ldaps://dc1.example.org:636` |
| Bind-DN | `cn=postwerk,ou=services,dc=example,dc=org` | `CN=Postwerk,OU=Dienste,DC=example,DC=org` |
| Basis-DN | `ou=people,dc=example,dc=org` | `DC=example,DC=org` |
| Benutzerfilter | `(&(objectClass=person)(mail=%s))` | `(&(objectClass=user)(mail=%s)(!(userAccountControl:1.2.840.113556.1.4.803:=2)))` |
| E-Mail / Name | `mail` / `cn` | `mail` / `displayName` |
| Admin-Filter | `memberOf=cn=mail-admins,ou=groups,dc=example,dc=org` | `memberOf=CN=Mail-Admins,OU=Gruppen,DC=example,DC=org` |

- Das Passwort wird nie in einen Suchfilter geschrieben: Postwerk sucht den Benutzer mit dem
  Dienstkonto und meldet sich dann als dieser Benutzer am Verzeichnis an.
- Ohne `ldaps://` oder StartTLS gehen Passwörter unverschlüsselt über das Netz.
- „Benutzer suchen“ zeigt alle gefundenen Attribute an (ohne Passwort-Attribute) – praktisch, um
  Filter und Attributnamen zu prüfen.
- Der **Abgleich** listet alle Verzeichnis-Benutzer mit E-Mail-Adresse, legt auf Wunsch fehlende Konten
  an und deaktiviert LDAP-Konten, deren Benutzer nicht mehr im Verzeichnis stehen (wenn „Entfernte
  Benutzer deaktivieren“ eingeschaltet ist).
- Ist das Verzeichnis nicht erreichbar, schlagen Anmeldungen von LDAP-Konten fehl; App-Passwörter
  funktionieren weiter. Lokale Konten sind nicht betroffen.
