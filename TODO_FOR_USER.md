# TODO_FOR_USER

Dinge, die nur der Mensch tun kann. Der Agent trägt hier ein statt zu blockieren oder zu
simulieren. Erledigtes bitte abhaken, nicht löschen — der Verlauf ist Dokumentation.

Legende: 🔴 blockiert eine Phase · 🟡 vor Phasenabschluss nötig · ⚪ nice to have

---

## Offen

### ⚪ GitHub-Remote — erledigt in Phase 0
- [x] Remote `origin` → `https://github.com/Elias02345/remote-agent.git` verbunden.
      `gh` war authentifiziert, initialer Push erfolgt.

### 🔴 D-04: WebAuthn-Domain final festlegen (blockiert Phase 6 und 7)
- [ ] Domain/Subdomain benennen, unter der der Owner-Login dauerhaft läuft
      (z. B. `remote.deine-domain.de`).
      **Ein späterer Wechsel macht alle registrierten Passkeys ungültig.**
- [ ] Domain in Cloudflare aktiv (Nameserver auf Cloudflare zeigend).

### 🔴 Cloudflare-API-Token für CloudGate (blockiert Phase 6)
- [ ] Token mit Rechten `Zone:DNS:Edit` + `Account:Cloudflare Tunnel:Edit` erzeugen
      und in der CloudGate-WebUI hinterlegen. **Nicht ins Repo.**

### 🔴 Zielserver bereitstellen (blockiert die Verifikation von Phase 1–3)
- [ ] Arch-Linux-Maschine (bare metal oder Proxmox-VM, siehe D-02) aufsetzen.
- [ ] Initialen SSH-Zugang als root/erster User herstellen, damit das
      Provisioning-Skript einmal ausgeführt werden kann.
- [ ] Öffentlichen Ed25519-Key des eigenen Rechners bereitlegen — das Skript trägt ihn
      für den `admin`-User ein. Ohne den Key sperrst du dich nach der SSH-Härtung aus.

### 🟡 GitHub-Key des Servers hinterlegen (vor Abschluss Phase 2)
- [ ] Der vom Provisioning erzeugte Ed25519-Public-Key des `agent`-Users muss unter
      GitHub → Settings → SSH and GPG keys eingetragen werden (Account-Key, kein
      Deploy-Key pro Repo).

### 🟡 ntfy-Kanal (vor Abschluss Phase 3)
- [ ] Entweder self-hosted ntfy oder ein Topic auf ntfy.sh festlegen und die URL in
      `.env` als `NTFY_URL` eintragen. Topic-Name muss geraten-sicher sein
      (ntfy.sh-Topics sind öffentlich, wenn man den Namen kennt).

### 🟡 FIDO2-Hardware-Key kaufen (vor Abschluss Phase 9)
- [ ] YubiKey o. ä. mit USB **und** NFC — schließt die Linux-Desktop-Passkey-Lücke und
      funktioniert gleichzeitig als Roaming-Authenticator auf Android.

### ⚪ Restic-Repository-Passwort
- [ ] Wird in Phase 5 vom Installer erzeugt. Der Mensch muss es **außerhalb des Servers**
      sichern (Passwortmanager). Verlust = Verlust aller Backups.

### ⚪ GitHub-Wiki aktivieren
- [ ] Falls der automatische Wiki-Push in Phase 0 fehlgeschlagen ist: im Repo unter
      Settings → Features → Wikis aktivieren und eine erste Seite über die Weboberfläche
      anlegen. Danach genügt `bash wiki/publish.sh`, um `wiki/` erneut zu publizieren.
