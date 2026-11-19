import { createContext, useContext } from "react";

// What the pointer or the selection points at, so nodes and edges can light up their neighbourhood
// and dim the rest without the graph being rebuilt.
export interface Focus {
  // Node ids and edge ids lit; null when nothing is focused.
  nodes: Set<string> | null;
  edges: Set<string> | null;
  selectedNode: string | null;
  selectedEdge: string | null;
  // The edge under the pointer, whose label shows whatever else is lit.
  hoveredEdge: string | null;
  // Every label shown, not only the lit ones: for the visual check.
  allLabels: boolean;
  hoverEdge: (id: string | null) => void;
  selectEdge: (id: string) => void;
}

export const FocusContext = createContext<Focus>({
  nodes: null,
  edges: null,
  selectedNode: null,
  selectedEdge: null,
  hoveredEdge: null,
  allLabels: false,
  hoverEdge: () => {},
  selectEdge: () => {},
});

export function useFocus() {
  return useContext(FocusContext);
}
