import logging
import os
import sqlite3
import tempfile
from datetime import datetime, timezone

from apscheduler.schedulers.background import BackgroundScheduler

import db as _db

logger = logging.getLogger(__name__)

GITHUB_TOKEN = os.getenv("GITHUB_TOKEN")
BACKUP_REPO = os.getenv("BACKUP_REPO")
BACKUP_BRANCH = os.getenv("BACKUP_BRANCH", "backups")
BACKUP_CRON = os.getenv("BACKUP_CRON", "0 2 * * *")
BACKUP_ENCRYPT_KEY = os.getenv("BACKUP_ENCRYPT_KEY")


def validate_backup_config() -> None:
    if BACKUP_REPO and not BACKUP_ENCRYPT_KEY:
        raise RuntimeError(
            "BACKUP_REPO is set but BACKUP_ENCRYPT_KEY is missing — "
            "refusing to push unencrypted backup to GitHub"
        )


def _do_backup() -> None:
    if not GITHUB_TOKEN or not BACKUP_REPO:
        logger.info("backup skipped: GITHUB_TOKEN or BACKUP_REPO not configured")
        return

    tmp_fd, tmp_path = tempfile.mkstemp(suffix=".db")
    os.close(tmp_fd)
    try:
        # 1. SQLite Online Backup API → consistent snapshot under WAL
        src = sqlite3.connect(_db.DB_PATH)
        dst = sqlite3.connect(tmp_path)
        src.backup(dst)
        src.close()
        dst.close()

        # 2. age encrypt with pyrage
        import pyrage

        recipient = pyrage.x25519.Recipient.from_str(BACKUP_ENCRYPT_KEY)
        with open(tmp_path, "rb") as f:
            plaintext = f.read()
        encrypted = pyrage.encrypt(plaintext, [recipient])

        # 3. Push commit to GitHub
        from github import Github

        ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        filepath = f"backups/ieops-mem-{ts}.db.age"
        gh = Github(GITHUB_TOKEN)
        repo = gh.get_repo(BACKUP_REPO)
        try:
            existing = repo.get_contents(filepath, ref=BACKUP_BRANCH)
            repo.update_file(filepath, f"backup {ts}", encrypted, existing.sha, branch=BACKUP_BRANCH)
        except Exception:
            repo.create_file(filepath, f"backup {ts}", encrypted, branch=BACKUP_BRANCH)

        logger.info("backup completed: %s", filepath)
    except Exception:
        logger.exception("backup failed")
    finally:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass


def start_scheduler() -> BackgroundScheduler:
    validate_backup_config()
    scheduler = BackgroundScheduler()

    parts = BACKUP_CRON.split()
    if len(parts) == 5:
        minute, hour, day, month, dow = parts
        scheduler.add_job(
            _do_backup,
            "cron",
            minute=minute,
            hour=hour,
            day=day,
            month=month,
            day_of_week=dow,
        )
    else:
        logger.warning("invalid BACKUP_CRON '%s' — backup scheduling skipped", BACKUP_CRON)

    scheduler.start()
    return scheduler
