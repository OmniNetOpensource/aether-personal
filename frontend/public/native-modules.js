export default function (_modules, call) {
  return {
    session(callback) { call('session').then(callback); },
    saveSession(token, email, callback) { call('saveSession', { token, email }).then(callback); },
    clearSession() { void call('clearSession'); },
    uuid(callback) { call('uuid').then(callback); },
    openLink(url) { void call('openLink', { url }); },
    resizeComposer() { void call('resizeComposer'); },
    openDesktop(token) { void call('openDesktop', { token }); },
    connect(url, protocols, options, id) { void call('connect', { url, protocols, id }); },
    send(message, id) { void call('send', { message, id }); },
    close(code, reason, id) { void call('close', { code, reason, id }); },
    ping() {},
  };
}
