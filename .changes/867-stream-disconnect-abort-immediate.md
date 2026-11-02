### fix(stream): abort upstream immediately on disconnect

Abort upstream HTTP requests and stream reader loops synchronously when downstream client context is canceled or disconnects, preventing lingering connections and wasted token quotas.
