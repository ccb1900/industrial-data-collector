import React, { useEffect } from "react";
import { commands, onObservation } from "./api/client";
import { useCollectionData } from "./hooks/useCollectionData";
import { Collections, Files, Sources } from "./components/Lists";

// Minimal P2 host verification page. It proves
// React -> Wails -> UI Host -> Query/Observation/Command, nothing more.
export default function App() {
  const { loading, sources, collections, files, error, refresh } =
    useCollectionData();

  useEffect(() => onObservation(() => void refresh()), [refresh]);

  const trigger = async () => {
    await commands.triggerCollection({ reason: "ui" });
    await refresh();
  };

  return (
    <main>
      <h1>Industrial Data Collector</h1>
      {loading && <p>Loading…</p>}
      {error && <p style={{ color: "red" }}>{error}</p>}
      <Sources items={sources} />
      <Collections items={collections} />
      <Files items={files} />
      <button onClick={() => void trigger()}>Trigger Collection</button>
    </main>
  );
}
