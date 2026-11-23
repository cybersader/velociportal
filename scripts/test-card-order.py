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

ICON = 'data:image/svg+xml,%3Csvg xmlns=%22http://www.w3.org/2000/svg%22 width=%2232%22 height=%2232%22/%3E'


def card(i):
    return f'<article class="card card-editable" data-service="Service {i}" data-service-id="{i}"><a class="service-main" href="#s{i}"><img class="service-icon" src="{ICON}" width="32" height="32" alt=""><span class="card-head"><span class="card-name">Service {i}</span></span></a></article>'


def fixture():
    page = (ROOT / 'handlers.go').read_text().split('const portalPage = `')[1].split('</html>`')[0] + '</html>'
    css = (ROOT / 'service_metadata_editor_ui.go').read_text().split('const serviceEditorCSS = `')[1].split('`')[0]
    # Production card shape: an article holding a real link and image.
    cards = ''.join(card(i) for i in (1, 2, 3))
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
            def centers(): return js('return [...document.querySelectorAll("#services .card")].map(c=>{let r=c.getBoundingClientRect();return {x:Math.round(r.left+r.width/2),y:Math.round(r.top+r.height/2)}})')
            def hold_drag(a, b, wait, kind='mouse'):
                req(session+'/actions', {'actions':[{'type':'pointer','id':'card-'+kind,'parameters':{'pointerType':kind},'actions':[{'type':'pointerMove',**a},{'type':'pointerDown','button':0},{'type':'pause','duration':wait},{'type':'pointerMove','duration':250,**b},{'type':'pause','duration':150},{'type':'pointerUp','button':0}]}]})
            c = centers(); req(session+'/actions', {'actions':[{'type':'pointer','id':'tap','parameters':{'pointerType':'mouse'},'actions':[{'type':'pointerMove',**c[1]},{'type':'pointerDown','button':0},{'type':'pointerUp','button':0}]}]})
            assert js('return location.hash')=='#s2' and order()=='123', 'a plain click must open the link without reordering'
            js('location.hash="#start"')
            c = centers(); hold_drag(c[0], c[2], 0); assert order()=='231', 'mouse drag on the page did not reorder'
            assert js('return location.hash')=='#start', 'dropping a dragged card must not open its link'
            assert not js('return document.querySelector("[data-lifted],[data-sorting],#services [style*=transform]")'), 'drag state left behind'
            req(session+'/refresh', {}); assert order()=='231', 'reload lost the dragged order'
            def server(ids):
                html = json.dumps(''.join(card(i) for i in ids))
                js('const old=document.getElementById("services"),n=old.cloneNode(false);n.innerHTML='+html+';old.replaceWith(n);document.body.dispatchEvent(new Event("htmx:afterSwap"))')
            server((1, 3)); assert order()=='31', 'a removed service must not discard the browser order'
            server((1, 2, 3)); assert order()=='231', 'browser order lost after a service came back'
            server((3, 1, 2)); assert order()=='312', 'a changed shared order must win over the browser order'
            assert js('return localStorage.length')==0, 'stale browser order kept after the shared order changed'
            js('localStorage.setItem("velociportal.card-order.v1."+"a".repeat(64), JSON.stringify(["npm:2","npm:1","npm:3"]))')
            req(session+'/refresh', {}); assert order()=='123', 'an order saved under the old key must not override the shared order'
            assert js('return localStorage.length')==0, 'order saved under the old key was kept'
            server((3, 1, 2)); assert order()=='312'
            c = centers(); hold_drag(c[0], c[2], 0, 'touch'); assert order()=='312', 'a touch swipe without holding must not reorder'
            c = centers(); hold_drag(c[2], c[0], 500, 'touch'); assert order()=='231', 'touch press-and-hold drag did not reorder'
            server((1, 2, 3)); assert order()=='123', 'shared order change did not reset the touch-saved order'
            open_order(); js('document.querySelector("#services .card").remove(); document.body.dispatchEvent(new Event("htmx:afterSwap"))'); assert not js('return document.querySelector("#card-order").open') and order()=='23'
            js('document.querySelector("#portal-content").dataset.identityScope="other";document.body.dispatchEvent(new Event("htmx:afterSwap"))'); assert js('return document.querySelector("#card-order-open").hidden')
            print('PASS account name, Cancel, pointer drag, page drag (mouse, click-through, touch hold), shared-order precedence, arrows, Save, reload, default order, visibility and identity changes')
        finally:
            try:
                if session: req(session, method='DELETE')
            finally: driver.terminate(); driver.wait(timeout=10)

if __name__ == '__main__': run()
