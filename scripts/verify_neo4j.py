#!/usr/bin/env python3
"""
Verify Neo4j connection and setup.

Usage:
    python scripts/verify_neo4j.py
"""

import sys
from pathlib import Path

# Add parent directory to path
sys.path.insert(0, str(Path(__file__).parent.parent))

from qatlas.knowledge import Neo4jClient


def main():
    """Verify Neo4j connection."""
    print("🔍 Verifying Neo4j connection...")
    print("-" * 50)
    
    try:
        client = Neo4jClient()
        client.connect()
        
        if client.test_connection():
            print("✅ Successfully connected to Neo4j!")
            
            # Get stats
            stats = client.get_stats()
            print(f"\n📊 Database stats:")
            for label, count in stats.items():
                print(f"   {label}: {count}")
            
            client.close()
            print("\n✨ Neo4j is ready to use!")
            return 0
        else:
            print("❌ Connection test failed")
            return 1
            
    except Exception as e:
        print(f"❌ Error connecting to Neo4j: {e}")
        print("\n💡 Make sure Neo4j is running:")
        print("   docker-compose up -d")
        return 1


if __name__ == "__main__":
    sys.exit(main())
