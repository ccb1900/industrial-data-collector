import { UITriggerRequest } from "../models/types";
import { invoke } from "./transport";

// Command Bridge: React -> Wails -> plugins/ui Host -> CollectionCommand.
// Returns once the command is accepted; completion arrives via Observation.
export const commands = {
  triggerCollection: (req: UITriggerRequest) =>
    invoke<void>("TriggerCollection", req),
};
