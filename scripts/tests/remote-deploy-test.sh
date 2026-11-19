#!/usr/bin/env bash
# 无真实 SSH/SCP，不执行生成的远端命令；所有测试文件仅写入 /tmp。
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
python3 - "$SCRIPT_DIR/../remote_deploy.sh" "${1:-$SCRIPT_DIR/../remote_deploy.sh}" <<'PY'
import os, pathlib, shlex, subprocess, sys, tempfile

script = pathlib.Path(sys.argv[1]).resolve()
source = pathlib.Path(sys.argv[2]).read_text()
passed = failed = 0
stub = r'''
ssh() {
    local cmd="${*: -1}"
    printf '%s\0' "$cmd" >> "$TEST_LOG"
    case "$cmd" in
        *ControlPath*|exit) return 0;;
        true) return 0;;
        *mktemp*) echo /tmp/fns-rd.TEST01;;
        *fns-probe.sh*) echo 'Linux|x86_64|test|1000|sudo|systemd';;
        *'cat /opt/fast-note/.version'*) echo 3.8.0;;
        *'test -x'*|*'[ -x '*) [ "${TEST_BINARY:-0}" = 1 ];;
        *'inspect'*'fast-note-sync-service'*)
            [ "${TEST_DOCKER:-0}" = 1 ] || return 1
            case "$cmd" in *Running*) echo "${TEST_RUNNING:-false}";; *) echo fast-note-sync-service;; esac;;
        *'curl '*)
            [ "${TEST_HEALTH_FAIL:-0}" = 0 ] || return 1
            printf '{"data":{"status":"healthy","version":"%s"}}\n' "${TEST_VERSION:-3.8.0}";;
        *' install '*) [ "${TEST_INSTALL_FAIL:-0}" = 0 ];;
        *'stop'*|*'disable'*) [ "${TEST_STOP_FAIL:-0}" = 0 ];;
        *) return 0;;
    esac
}
scp() { printf 'SCP %s\n' "${*: -1}" >&2; }
sleep() { :; }
'''

def run(args, **env):
    with tempfile.TemporaryDirectory(prefix='fns-rd-test.', dir='/tmp') as tmp:
        log = pathlib.Path(tmp) / 'commands'
        log.touch()
        args = list(args)
        if '--tarball' in args:
            i = args.index('--tarball') + 1
            package = pathlib.Path(tmp) / args[i]
            package.touch()
            args[i] = str(package)
        result = subprocess.run(['bash', '-c', stub + '\n' + source, str(script), *args],
            env={**os.environ, 'TMPDIR': tmp, 'SSHPASS': '', 'TEST_LOG': str(log), **env},
            text=True, capture_output=True)
        return result.returncode, result.stdout + result.stderr, log.read_text().split('\0')[:-1]

def check(name, condition):
    global passed, failed
    if condition:
        passed += 1
        print('PASS:', name)
    else:
        failed += 1
        print('FAIL:', name)

def words(command):
    def split(text):
        lexer = shlex.shlex(text, posix=True, punctuation_chars=';&|()<>`')
        lexer.whitespace_split = True
        lexer.commenters = ''
        return list(lexer)
    try:
        result = split(command)
    except ValueError:
        return []
    if result[:2] == ['bash', '-c']:
        return split(result[2])
    return result

for port in ['9000;id', 'abc', '70000', '0', '-1']:
    rc, out, commands = run(['host', '--http-port', port])
    check('拒绝端口 ' + port, rc != 0 and not commands and '端口' in out)
for port in ['1', '65535', '09000']:
    rc, out, commands = run(['host', '--http-port', port])
    check('接受合法端口 ' + port, rc == 0)
for package in ['pkg;id;.tar.gz', 'a b.tar.gz', '$(touch x).tar.gz', "a'\"b.tar.gz", '`id`.tar.gz']:
    rc, out, commands = run(['host', '--tarball', package])
    install = [c for c in commands if ' install ' in c]
    argv = words(install[0]) if install else []
    expected = '/tmp/fns-rd.TEST01/' + package
    check('安装包按单个普通参数传递 ' + package,
          rc == 0 and '--tarball' in argv and argv[argv.index('--tarball') + 1] == expected
          and '&&' not in argv)
    # SCP 仅上传到固定目录，避免旧版 SCP 的远端 shell 解析原始包名。
    check('SCP 目标不含包名 ' + package, package not in out.split('SCP ')[-1].splitlines()[0])
rc, out, commands = run(['host', '--version', '3.8.0'])
check('版本传入安装器且无重复自启', any('3.8.0' in words(c) for c in commands if ' install ' in c)
      and not any('enable-autostart' in c for c in commands))
rc, out, commands = run(['host', '--version', '3.8.0'], TEST_VERSION='3.7.0')
check('运行版本不符时报错', rc != 0 and '版本' in out)
rc, out, commands = run(['host', '--version', 'v3.8.0'])
check('版本允许 v 前缀', rc == 0)
for arg in [['host;id'], ['host', '-u', 'root;id'], ['host', '--version', '3.8.0\nBAD'], ['host', '-p', '22;id']]:
    rc, out, commands = run(arg)
    check('拒绝连接/版本参数注入 ' + repr(arg), rc != 0 and not commands)
for mode, env in [('docker', {'TEST_BINARY': '1'}), ('binary', {'TEST_DOCKER': '1'})]:
    rc, out, commands = run(['host', '--mode', mode], **env)
    check(mode + ' 切换保留 force 保护', rc != 0 and not any('stop ' in c for c in commands))
    rc, out, commands = run(['host', '--mode', mode, '--force'], **env)
    stops = [i for i,c in enumerate(commands) if 'stop ' in c or 'disable' in c]
    starts = [i for i,c in enumerate(commands) if ' install ' in c or 'up -d' in c]
    check(mode + ' 切换先停后启', rc == 0 and bool(stops) and bool(starts) and max(stops) < min(starts))
    rc, out, commands = run(['host', '--mode', mode, '--force'], TEST_STOP_FAIL='1', **env)
    check(mode + ' 停止失败中止', rc != 0 and not any(' install ' in c or 'up -d' in c for c in commands))
rc, out, commands = run(['host', '--force'], TEST_DOCKER='1', TEST_RUNNING='true')
check('旧容器仍运行则不安装', rc != 0 and not any(' install ' in c for c in commands))
rc, out, commands = run(['host', '--mode', 'docker', '--force'], TEST_BINARY='1')
stop = next((c for c in commands if 'disable' in c), '')
check('停止二进制包含自启禁用、状态与进程退出确认',
      all(token in stop for token in ['disable', 'is-active', 'pgrep', 'pkill', 'exit']))
for version in ['3.8.0', '3.7.0']:
    rc, out, commands = run(['host', '--version', '3.8.0'], TEST_VERSION=version)
    check('成功/失败均精确清理 ' + version,
          any('fns-rd.TEST01' in c and ('rmdir' in c or 'rm ' in c or '-delete' in c) for c in commands)
          and not any('/tmp/fns-rd.*' in c for c in commands))
for failure in ['TEST_INSTALL_FAIL', 'TEST_HEALTH_FAIL']:
    rc, out, commands = run(['host'], **{failure: '1'})
    check(failure + ' 退出清理', rc != 0 and any('fns-rd.TEST01' in c and '-delete' in c for c in commands))
print(f'{passed} passed, {failed} failed')
sys.exit(bool(failed))
PY
