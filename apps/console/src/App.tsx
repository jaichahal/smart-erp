import { FormEvent, useState } from "react";
import { Home } from "./Home";
import { openSession, type Profile } from "./session";

export function App() {
  const [loginName, setLoginName] = useState("");
  const [password, setPassword] = useState("");
  const [profile, setProfile] = useState<Profile | null>(null);
  const [error, setError] = useState("");

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      setProfile(await openSession(loginName, password));
    } catch (cause) {
      setProfile(null);
      setError(cause instanceof Error ? cause.message : "Sign-in failed");
    }
  }

  return (
    <main>
      <h1>Smart ERP</h1>
      {profile ? (
        <Home profile={profile} />
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
