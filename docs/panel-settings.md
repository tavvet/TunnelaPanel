[Project](../README.md) · **English** · [Русский](./panel-settings.ru.md)

# Panel settings

The panel opens in English by default, including on existing installations after an update if no language preference has been saved. It does not detect the browser's language.

## Interface language

Sign in as the administrator and open **Settings → Interface → Language**. Select **English** or **Русский**, then click **Save settings**. In Russian, the same section is **Настройки → Интерфейс → Язык**, with the **Сохранить настройки** button.

The choice applies to all administrator sessions and the login page. It persists across panel restarts and container recreation. Saving the language takes effect without restarting the panel or TrustTunnel, interrupting VPN connections, or applying or discarding the VPN user draft.

Panel pages and controls use the selected language. Log entries and diagnostics returned by TrustTunnel or the endpoint agent retain their original language.

## Storage and configuration boundaries

The language is a panel preference. It is separate from the [TrustTunnel TOML configuration](./configuration.md) and deployment settings in `deploy/.env`, such as public addresses, ports, cookies and trusted proxies. Interface language is currently the only panel preference exposed in the **Interface** section.

The panel saves the preference to `settings.json` in its `StateDir`, configured through `PANEL_STATE_DIR`. With the supplied Compose configuration, `/data/panel/settings.json` inside the container is `runtime/panel/settings.json` on the server. Its format is:

```json
{"language":"en"}
```

The supported values are `en` and `ru`. When the file is absent, the panel uses `en`. Use the panel to save the preference.

Back up the whole `runtime/` directory, including `runtime/panel/`, to retain the language setting together with administrator data. Automatic VPN apply backups do not include panel preferences. See [operations](./operations.md#persistent-data-and-backups) for backup scope and update instructions.
