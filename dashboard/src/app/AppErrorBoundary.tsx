import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props { children: ReactNode }
interface State { error: Error | null }

export class AppErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(_error: Error, _info: ErrorInfo) {
    // Exceptions may contain request material. Keep the browser diagnostic safe;
    // authoritative details belong in the daemon's redacted logs.
    console.error("DARKSTAR dashboard render failed; reload to recover.");
  }

  render() {
    if (!this.state.error) return this.props.children;

    return (
      <main className="fatal-error" aria-labelledby="fatal-error-title">
        <div className="fatal-error__mark" aria-hidden="true"><span /></div>
        <p className="eyebrow">Dashboard interrupted</p>
        <h1 id="fatal-error-title">This view couldn’t be rendered.</h1>
        <p>
          Your workflows continue in the local daemon. Reload the dashboard to
          request a fresh view of its authoritative state.
        </p>
        <button className="button button--primary" type="button" onClick={() => window.location.reload()}>
          Reload dashboard
        </button>
      </main>
    );
  }
}
