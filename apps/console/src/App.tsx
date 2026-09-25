import { FormEvent, useState } from "react";
import { signIn } from "./session";

export function App() {
  const [loginName, setLoginName] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      setName(await signIn(loginName, password));
    } catch (cause) {
      setName("");
      setError(cause instanceof Error ? cause.message : "Sign-in failed");
    }
  }

  return (
    <main>
      <h1>Smart ERP</h1>
      {name ? (
        <p data-testid="signed-in-name">{name}</p>
      ) : (
        <form onSubmit={onSubmit}>
          <label>
            Login name
            <input value={loginName} onChange={(event) => setLoginName(event.target.value)} autoComplete="username" />
          </label>
          <label>
            Password
            <input
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="current-password"
            />
          </label>
          <button type="submit">Sign in</button>
          {error ? <p role="alert">{error}</p> : null}
        </form>
      )}
    </main>
  );
}
