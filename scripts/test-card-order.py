"""Exercise the shipped header/dialog and card-order JS in a disposable Chrome profile.

Run with python3 scripts/test-card-order.py on a host with Chrome and chromedriver.
No package install, external service or live portal is needed.
"""
import json
import re
import socket
import subprocess
import tempfile
import time
from pathlib import Path
from urllib.request import Request, build_opener, ProxyHandler

ROOT = Path(__file__).resolve().parents[1]

def fixture():
    page = (ROOT / 'handlers.go').read_text().split('const portalPage = `')[1].split('</html>`')[0] + '</html>'
    css = (ROOT / 'service_metadata_editor_ui.go').read_text().split('const serviceEditorCSS = `')[1].split('`')[0]
    cards = ''.join(f'<a class="card" data-service="Service {i}" data-service-id="{i}"><span class="card-name">Service {i}</span></a>' for i in (1, 2, 3))
    for key, value in {'USER_NAME':'SampleName', 'USER_LOGIN':'sample@example.test', 'SERVICES_CLASS':'grid', 'SERVICES_BODY':cards, 'LOGO_PREF_SCOPE':'a'*64, 'SERVICE_EDITOR_CSS':css}.items():
        page = page.replace('{{'+key+'}}', value)
    page = re.sub(r'\{\{[A-Z_]+\}\}', '', page)
    page = page.replace('<script src="/static/htmx.min.js"></script>', '')
    # Keep defer semantics of the production editor.
    return page.replace('/static/card-order.js', 'card-order.js')


def run():
    with tempfile.TemporaryDirectory() as tmp, socket.socket() as sock:
        d = Path(tmp); (d/'portal.html').write_text(fixture()); (d/'card-order.js').write_bytes((ROOT/'assets/card-order.js').read_bytes())
        sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]; sock.close()
        driver = subprocess.Popen(['chromedriver', f'--port={port}'], stdout=subprocess.DEVNULL)
        http = build_opener(ProxyHandler({})); base = f'http://127.0.0.1:{port}'
        def req(path, data=None, method=None):
            r = Request(base+path, data=json.dumps(data).encode() if data is not None else None, method=method, headers={'Content-Type':'application/json'})
            with http.open(r, timeout=20) as answer: return json.load(answer)['value']
        session = None
        try:
            for _ in range(60):
                try: req('/status'); break
                except OSError: time.sleep(.1)
            session = '/session/'+req('/session', {'capabilities':{'alwaysMatch':{'browserName':'chrome','goog:chromeOptions':{'args':['--headless=new','--disable-background-networking',f'--user-data-dir={d}/profile']}}}})['sessionId']
            def js(src): return req(session+'/execute/sync', {'script':src,'args':[]})
            def click(id): js(f'document.getElementById("{id}").click()')
            def open_order(): click('account-trigger'); click('card-order-open'); assert js('return document.querySelector("#card-order").open')
            def order(): return js('return [...document.querySelectorAll("#services .card")].map(n=>n.dataset.serviceId).join("")')
            req(session+'/url', {'url':(d/'portal.html').as_uri()})
            for width in (805, 360):
                req(session+'/window/rect', {'width':width,'height':800})
                assert js('const n=document.querySelector("#account-trigger .user-name");return n.clientWidth>0 && n.scrollWidth<=n.clientWidth')
            open_order(); js('document.querySelector("[data-step=\\"1\\"]").click()'); click('card-order-cancel'); assert order()=='123'
            open_order()
            coords=js('return [...document.querySelectorAll(".order-grip")].map(g=>{let r=g.getBoundingClientRect();return {x:Math.round(r.left+r.width/2),y:Math.round(r.top+r.height*.75)}})')
            req(session+'/actions', {'actions':[{'type':'pointer','id':'grip','parameters':{'pointerType':'mouse'},'actions':[{'type':'pointerMove',**coords[0]},{'type':'pointerDown','button':0},{'type':'pointerMove','duration':250,**coords[2]},{'type':'pause','duration':120},{'type':'pointerUp','button':0}]}]})
            click('card-order-save'); assert order()=='231', 'drag did not reorder the cards'
            req(session+'/refresh', {}); assert order()=='231', 'reload lost saved order'
            open_order(); js('document.querySelector("[data-step=\\"1\\"]").click()'); click('card-order-save'); assert order()=='321'
            open_order(); click('card-order-default'); click('card-order-save'); assert order()=='123'
            open_order(); js('document.querySelector("#services .card").remove(); document.body.dispatchEvent(new Event("htmx:afterSwap"))'); assert not js('return document.querySelector("#card-order").open') and order()=='23'
            js('document.querySelector("#portal-content").dataset.identityScope="other";document.body.dispatchEvent(new Event("htmx:afterSwap"))'); assert js('return document.querySelector("#card-order-open").hidden')
            print('PASS account name, Cancel, pointer drag, arrows, Save, reload, default order, visibility and identity changes')
        finally:
            try:
                if session: req(session, method='DELETE')
            finally: driver.terminate(); driver.wait(timeout=10)

if __name__ == '__main__': run()
