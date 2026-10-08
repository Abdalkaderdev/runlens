import importlib.util
import os
import sys

_boot = os.path.dirname(os.path.abspath(__file__))

for _dir in list(sys.path):
    _file = os.path.join(_dir or os.getcwd(), "sitecustomize.py")
    if os.path.abspath(_dir or os.getcwd()) != _boot and os.path.isfile(_file):
        _spec = importlib.util.spec_from_file_location("_runlens_user_sitecustomize", _file)
        _mod = importlib.util.module_from_spec(_spec)
        sys.modules[_spec.name] = _mod
        _spec.loader.exec_module(_mod)
        break

sys.path.append(os.environ["RUNLENS_PY_SITE"])

_endpoint = os.environ.get("RUNLENS_OTLP_ENDPOINT")
_previous = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT")
if _endpoint:
    os.environ["OTEL_EXPORTER_OTLP_ENDPOINT"] = _endpoint

try:
    from opentelemetry.instrumentation.auto_instrumentation import initialize
    from opentelemetry.instrumentation.dbapi import TracedConnectionProxy

    initialize()

    if "execute" not in vars(TracedConnectionProxy):

        def _execute(self, *args, **kwargs):
            if not hasattr(self.__wrapped__, "execute"):
                raise AttributeError("execute")
            return self.cursor().execute(*args, **kwargs)

        def _executemany(self, *args, **kwargs):
            if not hasattr(self.__wrapped__, "executemany"):
                raise AttributeError("executemany")
            return self.cursor().executemany(*args, **kwargs)

        TracedConnectionProxy.execute = _execute
        TracedConnectionProxy.executemany = _executemany
except Exception as _err:
    print(f"runlens: python instrumentation disabled: {_err}", file=sys.stderr)
finally:
    if _endpoint:
        if _previous is None:
            os.environ.pop("OTEL_EXPORTER_OTLP_ENDPOINT", None)
        else:
            os.environ["OTEL_EXPORTER_OTLP_ENDPOINT"] = _previous
