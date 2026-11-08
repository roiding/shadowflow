import argparse
import getpass
import json
import os
from pathlib import Path
import re
import stat
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description='Store THS credentials for the official username/password login')
    parser.add_argument('path', type=Path, help='absolute path inside a private directory')
    arguments = parser.parse_args()
    destination = arguments.path
    if not destination.is_absolute() or not sys.stdin.isatty():
        parser.error('run interactively and use an absolute credential path')
    os.umask(0o077)
    try:
        parent_metadata = destination.parent.lstat()
    except FileNotFoundError:
        destination.parent.mkdir(parents=True, mode=0o700)
        parent_metadata = destination.parent.lstat()
    if not stat.S_ISDIR(parent_metadata.st_mode) or stat.S_ISLNK(parent_metadata.st_mode) or parent_metadata.st_mode & 0o077:
        parser.error('credential directory must have permissions 0700')
    if destination.exists() or destination.is_symlink():
        try:
            metadata = destination.lstat()
        except OSError:
            parser.error('credential file must be a regular file')
        if not stat.S_ISREG(metadata.st_mode) or destination.is_symlink():
            parser.error('credential file must be a regular file')
    username = input('同花顺用户名（官方密码登录不接受手机号/邮箱）: ').strip()
    if not username or len(username.encode()) > 100 or '@' in username or re.fullmatch(r'1\d{10}', username):
        parser.error('provide the THS username, not phone number or email')
    password = getpass.getpass('密码（不回显）: ')
    repeated = getpass.getpass('再次输入密码: ')
    if not password or password != repeated or len(password.encode()) > 256:
        parser.error('passwords do not match, are empty, or exceed the size limit')
    descriptor, temporary = tempfile.mkstemp(prefix='.ths-credentials-', dir=destination.parent)
    try:
        with os.fdopen(descriptor, 'w') as stream:
            json.dump({'username': username, 'password': password}, stream)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, destination)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    print('凭据已保存为仅本机当前用户可读的文件。')


if __name__ == '__main__':
    main()
