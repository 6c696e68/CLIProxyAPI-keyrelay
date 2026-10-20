"""Prepare local administration pages without enabling the missing model backend."""

from pathlib import Path
import re
import secrets
import shutil
import sqlite3

import argparse
from codex_client import private_directory

REPO = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--workspace-root", type=Path, default=REPO)
ROOT = parser.parse_args().workspace_root.resolve()
RUNTIME = ROOT / ".keyrelay-runtime"
private_directory(RUNTIME)

for folder in ("plugins/windows/amd64", "auth", "logs", "key-shop"):
    (RUNTIME / folder).mkdir(parents=True, exist_ok=True)

for name in ("key-shop", "ch-relay-vippro"):
    source = ROOT / ".keyrelay-tools/keyrelay-build/plugins/windows/amd64" / f"{name}.dll"
    target = RUNTIME / "plugins/windows/amd64" / source.name
    shutil.copy2(source, target)

database = RUNTIME / "key-shop/key-shop.db"
if not database.exists():
    shutil.copy2(REPO / "examples/keyrelay/key-shop.sample.db", database)
with sqlite3.connect(f"file:{database.as_posix()}?mode=ro", uri=True) as db:
    count = db.execute("SELECT COUNT(*) FROM keys").fetchone()[0]
if count < 1:
    raise RuntimeError("The sample key-shop database has no keys")

config_path = RUNTIME / "config.yaml"
secret_path = RUNTIME / ".secrets"
if config_path.exists():
    if not secret_path.exists():
        raise RuntimeError("Existing config has no local .secrets file; refusing to overwrite it")
    credentials = dict(line.split("=", 1) for line in secret_path.read_text().splitlines() if "=" in line)
    management = credentials["MGMT"]
    operator = credentials["API"]
else:
    management = "KR-" + secrets.token_urlsafe(24)
    operator = "kr_" + secrets.token_hex(24)
    config = (REPO / "examples/keyrelay/config.example.yaml").read_text(encoding="utf-8")
    for name, value in {
        "__INSTALL_ROOT__": RUNTIME.as_posix(), "__HOST__": "127.0.0.1", "__PORT__": "8319",
        "__OPERATOR_API_KEY__": operator, "__MANAGEMENT_PASSWORD__": management,
    }.items():
        config = config.replace(name, value)
    # Administration pages work independently; keep the unavailable generation route disabled.
    config, replaced = re.subn(r"(        ch-relay-vippro:\n.*?            enabled: )true",
                               r"\g<1>false", config, count=1, flags=re.S)
    if replaced != 1:
        raise RuntimeError("Could not disable the unavailable relay in the reference config")
    config = config.replace("    allow-remote: true", "    allow-remote: false")
    # The management frontend is served by Vite until its existing verification failures are resolved.
    config = config.replace("    disable-control-panel: false", "    disable-control-panel: true")
    secret_path.write_text(f"MGMT={management}\nAPI={operator}\n", encoding="utf-8")
    config_path.write_text(config, encoding="utf-8")

access = f"""KEYRELAY local administration
status: NOT READY
site_status: STARTING
install_root: {RUNTIME}
config: {config_path}
log: {RUNTIME / 'logs'}
auth_dir: {RUNTIME / 'auth'}
api_base: http://127.0.0.1:8319/v1
operator_api_key: {operator}
management_password: {management}
management_panel: http://127.0.0.1:5173
key_shop_admin: http://127.0.0.1:8319/v0/resource/plugins/key-shop/admin
customer_page: http://127.0.0.1:8319/client
vippro_dashboard: http://127.0.0.1:8319/v0/resource/plugins/ch-relay-vippro/dashboard
ch_relay: http://127.0.0.1:11500/v1
codex_plus_ready: false
key_shop_db: {database}
sample_key_label: key-vippro

Панель работает в режиме разработки. Адрес сервера для входа: http://127.0.0.1:8319
Для входа в панель и API Keys используй management_password, не operator_api_key.
Статус всего KEYRELAY остаётся NOT READY: ch-relay отсутствует и Plus login не выполнен.
Плагин ch-relay-vippro установлен, но отключён до выполнения этих условий.
key-shop включён, sample DB скопирована до первого запуска.
Секрет старого sample ck_ ключа неизвестен; новый клиентский ключ можно создать в API Keys.
Вход в Plus выполняется в Auth Files основной панели. Не копируй чужие auth-файлы.
Автоматическое скачивание другой management.html отключено.
Предыдущий альтернативный запуск не использует эту конфигурацию.
"""
(RUNTIME / "ACCESS.txt").write_text(access, encoding="utf-8")
(RUNTIME / "ADMIN-PASSWORD.txt").write_text(management + "\n", encoding="utf-8")
print(f"Local administration configured; sample keys present: {count}. Secrets saved in ACCESS.txt.")
