import json, http.server, sys
PORT=int(sys.argv[1])
class H(http.server.BaseHTTPRequestHandler):
  def log_message(self,*a): pass
  def do_POST(self):
    n=int(self.headers.get('Content-Length','0')); body=self.rfile.read(n)
    print('HIT',self.path,flush=True)
    if self.path.startswith('/userapi/v1/'):
      out={'code':0,'data':{'access_token':'at-smoke','refresh_token':'rt-smoke-2'}}
    elif self.path.endswith('/chat/completions'):
      print('MODEL',self.headers.get('X-Request-Model'),'VER',self.headers.get('X-Version'),'SIGN',self.headers.get('X-Auth-Sign'),flush=True)
      chunks=['{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Halo"},"finish_reason":null}]}',
              '{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" dari AutoClaw smoke"},"finish_reason":"stop"}]}']
      self.send_response(200); self.send_header('Content-Type','text/event-stream'); self.end_headers()
      for c in chunks: self.wfile.write(('data: '+c+'\n\n').encode()); self.wfile.flush()
      self.wfile.write(b'data: [DONE]\n\n'); self.wfile.flush(); return
    else:
      self.send_response(404); self.end_headers(); return
    raw=json.dumps(out).encode(); self.send_response(200); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(raw))); self.end_headers(); self.wfile.write(raw)
http.server.ThreadingHTTPServer(('0.0.0.0',PORT),H).serve_forever()
