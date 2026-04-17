"""
Server Configuration

Defines configuration for the FastAPI web server.
"""

import os
from typing import Optional
from pydantic import BaseModel


class ServerConfig(BaseModel):
    """Server configuration settings."""

    # Server settings
    host: str = "0.0.0.0"
    port: int = 8000
    debug: bool = False

    # Neo4j settings
    neo4j_uri: str = "bolt://localhost:7687"
    neo4j_user: str = "neo4j"
    neo4j_password: str = ""

    # Wiki settings
    wiki_dir: str = "wiki"
    raw_dir: str = "raw"

    # LLM settings
    openai_api_key: Optional[str] = None
    anthropic_api_key: Optional[str] = None

    @classmethod
    def from_env(cls) -> "ServerConfig":
        """Load configuration from environment variables."""
        return cls(
            host=os.getenv("SERVER_HOST", "0.0.0.0"),
            port=int(os.getenv("SERVER_PORT", "8000")),
            debug=os.getenv("SERVER_DEBUG", "false").lower() == "true",
            neo4j_uri=os.getenv("NEO4J_URI", "bolt://localhost:7687"),
            neo4j_user=os.getenv("NEO4J_USER", "neo4j"),
            neo4j_password=os.getenv("NEO4J_PASSWORD", ""),
            wiki_dir=os.getenv("WIKI_DIR", "wiki"),
            raw_dir=os.getenv("RAW_DIR", "raw"),
            openai_api_key=os.getenv("OPENAI_API_KEY"),
            anthropic_api_key=os.getenv("ANTHROPIC_API_KEY"),
        )

    def get_neo4j_config(self) -> dict:
        """Get Neo4j connection configuration."""
        return {
            "uri": self.neo4j_uri,
            "user": self.neo4j_user,
            "password": self.neo4j_password,
        }


# Global configuration instance
config: Optional[ServerConfig] = None


def get_config() -> ServerConfig:
    """Get or create global configuration."""
    global config
    if config is None:
        config = ServerConfig.from_env()
    return config